package services

import (
	"context"
	"errors"
	"net"
	"testing"

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
