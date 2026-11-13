package services

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestOpOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, outcomeOK},
		{"not found", ErrNotFound, outcomeNotFound},
		{"wrapped not found", wrapErr(ErrNotFound), outcomeNotFound},
		{"other", errors.New("tier is down"), outcomeError},
		{"deadline", context.DeadlineExceeded, outcomeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := opOutcome(c.err); got != c.want {
				t.Fatalf("opOutcome(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

type wrappedErr struct{ err error }

func (w wrappedErr) Error() string { return "wrapped: " + w.err.Error() }
func (w wrappedErr) Unwrap() error { return w.err }
func wrapErr(err error) error      { return wrappedErr{err} }

// Every call the Store makes into a tier is counted under that tier's own
// name with the outcome the tier answered — including the not_found that
// the walk skips over on the way to a lower tier.
func TestBackendOpsCounted(t *testing.T) {
	fast := newFakeProvider("fast-tier", true)
	broken := &failingPushProvider{fakeProvider: newFakeProvider("broken-tier", true), pushErr: errors.New("down")}
	store := NewStore([]StoreProvider{fast, broken})
	const h = "feed0001"

	pullNF := ops("fast-tier", "pull", outcomeNotFound)
	pushErr := ops("broken-tier", "push", outcomeError)
	pushOK := ops("fast-tier", "push", outcomeOK)

	if _, err := store.Push(context.Background(), h, []byte("d8:announce0:e")); err == nil {
		t.Fatal("expected push to fail on the broken tier")
	}
	if _, err := store.Pull(context.Background(), h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pull err = %v, want ErrNotFound", err)
	}

	// Push walks tiers in reverse: broken first, fails, fast never reached.
	if got := ops("broken-tier", "push", outcomeError) - pushErr; got != 1 {
		t.Fatalf("broken-tier push/error = %v, want 1", got)
	}
	if got := ops("fast-tier", "push", outcomeOK) - pushOK; got != 0 {
		t.Fatalf("fast-tier push/ok = %v, want 0", got)
	}
	if got := ops("fast-tier", "pull", outcomeNotFound) - pullNF; got != 1 {
		t.Fatalf("fast-tier pull/not_found = %v, want 1", got)
	}
}

func ops(backend, op, outcome string) float64 {
	return testutil.ToFloat64(backendOpsTotal.WithLabelValues(backend, op, outcome))
}

func manifests(result string) float64 {
	return testutil.ToFloat64(manifestTotal.WithLabelValues(result))
}

// The three ways a manifest request resolves at the tier layer each land in
// their own series: built on a miss, hit on a valid cached blob, rebuilt
// when the caller rejects what the tier holds.
func TestManifestResultsCounted(t *testing.T) {
	tier := newFakeProvider("tier", true)
	store := NewStore([]StoreProvider{tier})
	const h = "feed0002"
	_, _ = tier.Push(context.Background(), h, []byte("torrent"))
	build := func(torrent []byte) ([]byte, error) { return []byte("m:" + string(torrent)), nil }
	accept := func([]byte) bool { return true }
	reject := func([]byte) bool { return false }

	built, hit, rebuilt := manifests(manifestBuilt), manifests(manifestHit), manifests(manifestRebuilt)

	if _, err := store.ManifestIf(context.Background(), h, accept, build); err != nil {
		t.Fatal(err)
	}
	// The in-process map holds the result for 5 minutes; a fresh Store over
	// the same tier is how the next request reaches the tier again.
	if _, err := NewStore([]StoreProvider{tier}).ManifestIf(context.Background(), h, accept, build); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore([]StoreProvider{tier}).ManifestIf(context.Background(), h, reject, build); err != nil {
		t.Fatal(err)
	}

	if got := manifests(manifestBuilt) - built; got != 1 {
		t.Fatalf("built = %v, want 1", got)
	}
	if got := manifests(manifestHit) - hit; got != 1 {
		t.Fatalf("hit = %v, want 1", got)
	}
	if got := manifests(manifestRebuilt) - rebuilt; got != 1 {
		t.Fatalf("rebuilt = %v, want 1", got)
	}
}

// Fingerprints go through the same derived path but must not be counted as
// manifests.
func TestFingerprintNotCountedAsManifest(t *testing.T) {
	tier := newFakeProvider("tier", true)
	store := NewStore([]StoreProvider{tier})
	const h = "feed0003"
	_, _ = tier.Push(context.Background(), h, []byte("torrent"))
	built := manifests(manifestBuilt)
	if _, err := store.Fingerprint(context.Background(), h, func([]byte) ([]byte, error) { return []byte("fp"), nil }); err != nil {
		t.Fatal(err)
	}
	if got := manifests(manifestBuilt) - built; got != 0 {
		t.Fatalf("fingerprint counted as manifest: built delta = %v", got)
	}
}

// The gRPC server is instrumented end to end: a call over the wire lands in
// grpc_server_handled_total with the code the handler returned.
func TestGRPCHandledTotal(t *testing.T) {
	tier := newFakeProvider("tier", true)
	store := NewStore([]StoreProvider{tier})
	const h = "feed0004"
	_, _ = tier.Push(context.Background(), h, []byte("torrent"))

	srv := &GRPCServer{s: NewServer(store, nil, nil, nil)}
	gs := srv.newServer()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := pb.NewTorrentStoreClient(conn)

	okBefore := handledTotal(t, "Touch", codes.OK)
	nfBefore := handledTotal(t, "Touch", codes.NotFound)

	if _, err := cl.Touch(context.Background(), &pb.TouchRequest{InfoHash: h}); err != nil {
		t.Fatalf("Touch(present): %v", err)
	}
	_, err = cl.Touch(context.Background(), &pb.TouchRequest{InfoHash: "feed0000"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Touch(absent) code = %v, want NotFound", status.Code(err))
	}

	if got := handledTotal(t, "Touch", codes.OK) - okBefore; got != 1 {
		t.Fatalf("handled_total{Touch,OK} delta = %v, want 1", got)
	}
	if got := handledTotal(t, "Touch", codes.NotFound) - nfBefore; got != 1 {
		t.Fatalf("handled_total{Touch,NotFound} delta = %v, want 1", got)
	}
}

// handledTotal reads grpc_server_handled_total for TorrentStore/<method>
// with the given code off the default registry — the view /metrics serves.
func handledTotal(t *testing.T, method string, code codes.Code) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"grpc_service": "TorrentStore",
		"grpc_method":  method,
		"grpc_type":    "unary",
		"grpc_code":    code.String(),
	}
	for _, f := range families {
		if f.GetName() != "grpc_server_handled_total" {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if v, ok := want[lp.GetName()]; ok && v != lp.GetValue() {
					continue next
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// pushSeries reads every series Push can move, keyed "private/outcome".
func pushSeries() map[string]float64 {
	out := map[string]float64{}
	for _, private := range []bool{false, true} {
		for _, o := range []string{pushStored, pushMerged, pushKept, pushHeld, pushStripped, pushBlind, pushRefused, pushMalformed, pushError} {
			p := strconv.FormatBool(private)
			out[p+"/"+o] = testutil.ToFloat64(pushTotal.WithLabelValues(p, o))
		}
	}
	return out
}

// pushDelta pushes raw once and reports how far each series moved.
func pushDelta(t *testing.T, srv *Server, raw []byte) (map[string]float64, error) {
	t.Helper()
	before := pushSeries()
	_, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw})
	delta := pushSeries()
	for k := range delta {
		delta[k] -= before[k]
	}
	return delta, err
}

func infoHashOf(t *testing.T, raw []byte) string {
	t.Helper()
	pt, err := parseTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pt.mi.HashInfoBytes().HexString()
}

// abuseDown is an oracle that cannot be reached.
type abuseDown struct{}

func (abuseDown) Get(context.Context, string) (bool, error) { return false, errors.New("down") }
func (abuseDown) CheckFingerprint(context.Context, []byte) (bool, error) {
	return false, errors.New("down")
}

// Each way Push can end is counted exactly once, under the torrent's private
// flag and the outcome Push actually reached; no other series moves. The
// reply code is checked too, so a case that drifts onto another branch fails
// on the scenario rather than passing on the metric.
func TestPushOutcomesCounted(t *testing.T) {
	const trackerA, trackerB = "http://a.invalid/announce", "udp://b.invalid:80/announce"
	mem := func(defaults []string) (*Server, *fakeProvider) {
		p := newFakeProvider("mem", true)
		return NewServer(NewStore([]StoreProvider{p}), nil, nil, defaults), p
	}
	pushed := func(t *testing.T, srv *Server, raw []byte) {
		t.Helper()
		if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); err != nil {
			t.Fatalf("setup push: %v", err)
		}
	}
	// storedUnloadable stores bytes metainfo cannot load under raw's
	// infohash, so mergeTorrent fails on the stored copy.
	storedUnloadable := func(t *testing.T, raw []byte) (*Server, []byte) {
		srv, p := mem(nil)
		p.torrents[infoHashOf(t, raw)] = []byte("not a torrent")
		return srv, raw
	}
	malformedPrivate := func() metainfo.Info {
		i, yes := malformedInfo(), true
		i.Private = &yes
		return i
	}
	cases := []struct {
		name    string
		private bool
		outcome string
		code    codes.Code
		// setup returns the server and the torrent whose push is measured;
		// pushes setup makes itself are not counted against the case.
		setup func(t *testing.T) (*Server, []byte)
	}{
		{"public, nothing stored", false, pushStored, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			return srv, makeTorrent(t, trackerA, nil, nil, false)
		}},
		{"private, nothing stored", true, pushStored, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			return srv, makeTorrent(t, passkeyA, nil, nil, true)
		}},
		{"public, new tracker merged", false, pushMerged, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			pushed(t, srv, makeTorrent(t, trackerA, nil, nil, false))
			return srv, makeTorrent(t, trackerA, metainfo.AnnounceList{{trackerA}, {trackerB}}, nil, false)
		}},
		{"private, trackerless copy adopts upload", true, pushMerged, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem([]string{openTracker})
			pushed(t, srv, makeTorrent(t, openTracker, nil, nil, true))
			return srv, makeTorrent(t, passkeyA, nil, nil, true)
		}},
		{"public, nothing new", false, pushKept, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			pushed(t, srv, makeTorrent(t, trackerA, nil, nil, false))
			return srv, makeTorrent(t, trackerA, nil, nil, false)
		}},
		{"private, same upload again", true, pushKept, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			pushed(t, srv, makeTorrent(t, passkeyA, nil, nil, true))
			return srv, makeTorrent(t, passkeyA, nil, nil, true)
		}},
		{"private, trackerless push of stored copy", true, pushKept, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			pushed(t, srv, makeTorrent(t, passkeyA, nil, nil, true))
			return srv, makeTorrent(t, "", nil, nil, true)
		}},
		{"private, keep-first holds another upload", true, pushHeld, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			pushed(t, srv, makeTorrent(t, passkeyA, nil, nil, true))
			return srv, makeTorrent(t, passkeyB, nil, nil, true)
		}},
		// A copy stored before Push dropped default trackers is rewritten
		// without them; whether the upload was thrown away decides the outcome.
		{"private, same upload, stored copy cleaned", true, pushMerged, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, p := mem([]string{openTracker})
			raw := makeTorrent(t, passkeyA, nil, nil, true)
			p.torrents[infoHashOf(t, raw)] = makeTorrent(t, passkeyA, metainfo.AnnounceList{{passkeyA}, {openTracker}}, nil, true)
			return srv, raw
		}},
		{"private, another upload, stored copy cleaned", true, pushHeld, codes.OK, func(t *testing.T) (*Server, []byte) {
			srv, p := mem([]string{openTracker})
			raw := makeTorrent(t, passkeyB, nil, nil, true)
			p.torrents[infoHashOf(t, raw)] = makeTorrent(t, passkeyA, metainfo.AnnounceList{{passkeyA}, {openTracker}}, nil, true)
			return srv, raw
		}},
		{"public, stored copy unreadable", false, pushBlind, codes.OK, func(t *testing.T) (*Server, []byte) {
			p := &failingPullProvider{newFakeProvider("mem", true)}
			return NewServer(NewStore([]StoreProvider{p}), nil, nil, nil), makeTorrent(t, trackerA, nil, nil, false)
		}},
		{"public, stored copy unmergeable", false, pushBlind, codes.OK, func(t *testing.T) (*Server, []byte) {
			return storedUnloadable(t, makeTorrent(t, trackerA, nil, nil, false))
		}},
		{"private, stored copy unmergeable", true, pushBlind, codes.OK, func(t *testing.T) (*Server, []byte) {
			return storedUnloadable(t, makeTorrent(t, passkeyA, nil, nil, true))
		}},
		{"public, stoplist", false, pushRefused, codes.PermissionDenied, func(t *testing.T) (*Server, []byte) {
			info := validInfo()
			info.Name = "foo bar"
			return NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, mustStoplist(t, noExceptFixture), nil), makeRawTorrent(t, info)
		}},
		{"private, infohash banned", true, pushRefused, codes.PermissionDenied, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			raw := makeTorrent(t, passkeyA, nil, nil, true)
			srv.a = &fakeAbuse{byHash: map[string]bool{infoHashOf(t, raw): true}}
			return srv, raw
		}},
		{"public, payload banned", false, pushRefused, codes.PermissionDenied, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			raw := makeRawTorrent(t, validInfo())
			srv.a = &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{digestOf(t, raw): true}}
			return srv, raw
		}},
		{"public, bad geometry", false, pushMalformed, codes.InvalidArgument, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			return srv, makeRawTorrent(t, malformedInfo())
		}},
		{"private, bad geometry", true, pushMalformed, codes.InvalidArgument, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			return srv, makeRawTorrent(t, malformedPrivate())
		}},
		{"public, store write failed", false, pushError, codes.Unknown, func(t *testing.T) (*Server, []byte) {
			p := &failingPushProvider{fakeProvider: newFakeProvider("mem", true), pushErr: errors.New("down")}
			return NewServer(NewStore([]StoreProvider{p}), nil, nil, nil), makeTorrent(t, trackerA, nil, nil, false)
		}},
		{"private, stored copy unreadable", true, pushError, codes.Unavailable, func(t *testing.T) (*Server, []byte) {
			p := &failingPullProvider{newFakeProvider("mem", true)}
			return NewServer(NewStore([]StoreProvider{p}), nil, nil, nil), makeTorrent(t, passkeyA, nil, nil, true)
		}},
		{"private, abuse lookup failed", true, pushError, codes.Unknown, func(t *testing.T) (*Server, []byte) {
			srv, _ := mem(nil)
			srv.a = abuseDown{}
			return srv, makeTorrent(t, passkeyA, nil, nil, true)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, raw := c.setup(t)
			delta, err := pushDelta(t, srv, raw)
			if status.Code(err) != c.code {
				t.Fatalf("Push code = %v (err %v), want %v", status.Code(err), err, c.code)
			}
			want := strconv.FormatBool(c.private) + "/" + c.outcome
			for k, d := range delta {
				if k == want && d != 1 || k != want && d != 0 {
					t.Errorf("%s delta = %v, want %v", k, d, map[bool]int{true: 1, false: 0}[k == want])
				}
			}
		})
	}
}

// A torrent that does not parse has no private flag to label it with, so it
// is left to grpc_server_handled_total{grpc_code="InvalidArgument"} rather
// than counted under a guess.
func TestPushUnparsableNotCounted(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	delta, err := pushDelta(t, srv, []byte("not a torrent"))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Push code = %v, want InvalidArgument", status.Code(err))
	}
	for k, d := range delta {
		if d != 0 {
			t.Errorf("%s delta = %v, want 0", k, d)
		}
	}
}
