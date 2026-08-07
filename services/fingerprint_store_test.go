package services

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBuildAndParseFingerprintRoundTrip(t *testing.T) {
	torrent := makeMultiFileTorrent(t, "x", []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 1},
	})
	blob, err := buildFingerprint(torrent)
	if err != nil {
		t.Fatalf("buildFingerprint: %v", err)
	}
	got := parseFingerprint(blob)
	if len(got) == 0 {
		t.Fatal("round trip produced no fingerprints")
	}
	for _, f := range got {
		if len(f.Value) != 64 {
			t.Fatalf("round trip lost the digest: %+v", f)
		}
	}
	if strings.Contains(string(blob), ":") {
		t.Fatalf("blob should be bare hex now: %q", blob)
	}
}

// A future writer may add a fingerprint kind or a trailing column. Older
// readers must skip what they cannot parse instead of failing the whole read.
func TestParseFingerprintToleratesJunk(t *testing.T) {
	blob := []byte(strings.Join([]string{
		"abc\t123",
		"",
		"v1layout:legacy\t456", // blob written before the scheme label was dropped
		":\t1",                 // no digest at all
		"def\t789\textra-column-from-a-newer-writer",
		"nolength",
	}, "\n"))
	got := parseFingerprint(blob)
	if len(got) != 4 {
		t.Fatalf("parsed %d fingerprints, want 4: %+v", len(got), got)
	}
	if got[0].Value != "abc" || got[0].Length != 123 {
		t.Fatalf("first entry wrong: %+v", got[0])
	}
	if got[1].Value != "legacy" {
		t.Fatalf("legacy scheme prefix should be stripped: %+v", got[1])
	}
	if got[2].Value != "def" {
		t.Fatalf("extra column should be tolerated: %+v", got[2])
	}
	if got[3].Length != 0 {
		t.Fatalf("missing length should default to 0: %+v", got[3])
	}
}

// Mirrors TestStoreManifestBuildOnceAndBackfill: derive once, persist to the
// tiers that opt in, skip the one that opts out.
func TestStoreFingerprintBuildOnceAndCache(t *testing.T) {
	fast := newFakeProvider("fast", true)
	slow := newFakeProvider("slow", true)
	optOut := newFakeProvider("optout", false)
	store := NewStore([]StoreProvider{fast, slow, optOut})

	torrent := makeMultiFileTorrent(t, "x", []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 1},
	})
	const h = "deadbeef"
	_, _ = fast.Push(context.Background(), h, torrent)
	_, _ = slow.Push(context.Background(), h, torrent)

	var builds int
	build := func(b []byte) ([]byte, error) {
		builds++
		return buildFingerprint(b)
	}

	out, err := store.Fingerprint(context.Background(), h, build)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	// Persisting is off the request path now, so wait for it rather than
	// asserting immediately.
	waitFor(t, func() bool {
		fast.mu.Lock()
		slow.mu.Lock()
		defer fast.mu.Unlock()
		defer slow.mu.Unlock()
		return len(fast.fingerprints[h]) > 0 && len(slow.fingerprints[h]) > 0
	}, "fingerprint not cached in the opted-in tiers")
	optOut.mu.Lock()
	defer optOut.mu.Unlock()
	if len(optOut.fingerprints) != 0 {
		t.Fatal("opted-out tier must not cache fingerprints")
	}
	if len(parseFingerprint(out)) == 0 {
		t.Fatalf("cached blob does not parse back: %q", out)
	}
}

// A hit in a lower tier must backfill the faster ones, so the next read is
// served without walking down again.
func TestStoreFingerprintBackfillsUpperTier(t *testing.T) {
	fast := newFakeProvider("fast", true)
	slow := newFakeProvider("slow", true)
	store := NewStore([]StoreProvider{fast, slow})

	const h = "cafebabe"
	payload := []byte("abc\t123\n")
	_, _ = slow.PushDerived(context.Background(), DerivedFingerprint, h, payload)

	build := func([]byte) ([]byte, error) {
		t.Fatal("build must not run when a lower tier already has the value")
		return nil, nil
	}
	out, err := store.Fingerprint(context.Background(), h, build)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(payload) {
		t.Fatalf("got %q, want %q", out, payload)
	}
	if string(fast.fingerprints[h]) != string(payload) {
		t.Fatal("upper tier was not backfilled")
	}
}

// The RPC path: derive once, serve from cache after, and keep returning the
// same values. Mirrors the Files handler, so the same guarantees apply.
func TestServerFingerprintServesAndCaches(t *testing.T) {
	p := newFakeProvider("mem", true)
	raw := makeRawTorrent(t, validInfo())
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := mi.HashInfoBytes().HexString()
	p.torrents[h] = raw

	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)
	first, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: h})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if first.GetValue() == "" || first.GetLength() == 0 {
		t.Fatalf("malformed fingerprint in reply: %+v", first)
	}
	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.fingerprints[h]) > 0
	}, "derived fingerprint was not cached")

	// A second call must not need the torrent at all: drop it and check the
	// reply still comes back identical, proving it is served from the cache.
	delete(p.torrents, h)
	second, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: h})
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if second.GetValue() != first.GetValue() || second.GetLength() != first.GetLength() {
		t.Fatalf("cached reply differs: %+v vs %+v", second, first)
	}
}

func TestServerFingerprintUnknownHash(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	_, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: "nosuchhash"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v (err %v), want NotFound", status.Code(err), err)
	}
}

// The abuse lookup takes a raw digest. Anything that cannot be one yields nil
// rather than a query that could only ever miss.
func TestFingerprintDigestRejectsUnusable(t *testing.T) {
	good := strings.Repeat("ab", 32) // 64 hex chars -> 32 bytes
	for name, blob := range map[string]string{
		"not hex":     "nothex!!" + strings.Repeat("0", 56) + "\t1",
		"wrong width": strings.Repeat("ab", 16) + "\t1",
		"empty":       "",
	} {
		if d := fingerprintDigest([]byte(blob)); d != nil {
			t.Fatalf("%s: expected nil, got %x", name, d)
		}
	}
	if d := fingerprintDigest([]byte(good + "\t1")); len(d) != 32 {
		t.Fatalf("a usable digest was rejected: %x", d)
	}
	// A blob written before the scheme label was dropped still yields one.
	if d := fingerprintDigest([]byte("v1layout:" + good + "\t1")); len(d) != 32 {
		t.Fatalf("legacy blob rejected: %x", d)
	}
}

// fakeAbuse is a scripted blocking oracle.
type fakeAbuse struct {
	byHash    map[string]bool
	byDigest  map[string]bool
	err       error
	fpQueries int
}

func (f *fakeAbuse) Get(_ context.Context, h string) (bool, error) { return f.byHash[h], nil }

func (f *fakeAbuse) CheckFingerprint(_ context.Context, fp []byte) (bool, error) {
	f.fpQueries++
	if f.err != nil {
		return false, f.err
	}
	return f.byDigest[hex.EncodeToString(fp)], nil
}

// serverWithTorrent stores a torrent and returns a server wired to the oracle.
func serverWithTorrent(t *testing.T, a abuseChecker) (*Server, string, []byte) {
	t.Helper()
	p := newFakeProvider("mem", true)
	raw := makeRawTorrent(t, validInfo())
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := mi.HashInfoBytes().HexString()
	p.torrents[h] = raw
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)
	srv.a = a
	return srv, h, raw
}

// digestOf returns the digest the server will look up for this torrent.
func digestOf(t *testing.T, raw []byte) string {
	t.Helper()
	blob, err := buildFingerprint(raw)
	if err != nil {
		t.Fatalf("buildFingerprint: %v", err)
	}
	d := fingerprintDigest(blob)
	if len(d) != 32 {
		t.Fatalf("expected a 32-byte digest, got %d bytes", len(d))
	}
	return hex.EncodeToString(d)
}

// The whole point: an infohash nobody ever reported is refused because its
// payload is blocked under some other one.
func TestPayloadBlockedUnderAnotherInfohash(t *testing.T) {
	a := &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{}}
	srv, h, raw := serverWithTorrent(t, a)
	a.byDigest[digestOf(t, raw)] = true

	// Pull and Push hold the torrent bytes, so they block on the first call.
	// Files answers from the cached manifest and refuses to fetch a torrent
	// just to fingerprint it, so it blocks only once the fingerprint is warm —
	// the first call goes through and warms it in the background.
	if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); err != nil {
		t.Fatalf("Files: first call should pass while the fingerprint is cold, got %v", err)
	}
	// Pull derives it from the bytes it already holds; that is what warms
	// the cache Files then reads.
	if _, err := srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Pull: code = %v, want PermissionDenied", status.Code(err))
	}
	waitFor(t, func() bool {
		_, err := srv.s.CachedFingerprint(context.Background(), h)
		return err == nil
	}, "Pull did not persist the fingerprint")
	if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Files: code = %v (err %v), want PermissionDenied once warm", status.Code(err), err)
	}
	if _, err := srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Pull: code = %v (err %v), want PermissionDenied", status.Code(err), err)
	}
	// And refused at ingest, so it is never stored in the first place.
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Push: code = %v (err %v), want PermissionDenied", status.Code(err), err)
	}
}

// Negative control: with the same wiring but the payload not blocked, all
// three succeed. Without this the test above would pass on any failure.
func TestPayloadNotBlockedPassesThrough(t *testing.T) {
	a := &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{}}
	srv, h, raw := serverWithTorrent(t, a)

	if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); err != nil {
		t.Fatalf("Files: %v", err)
	}
	if _, err := srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if a.fpQueries == 0 {
		t.Fatal("payload was never actually looked up")
	}
}

// A flaky lookup must not take the service down: the infohash arm is the hard
// gate and has already run.
func TestPayloadLookupFailureIsNotFatal(t *testing.T) {
	a := &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{}, err: errors.New("upstream down")}
	srv, h, _ := serverWithTorrent(t, a)
	if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); err != nil {
		t.Fatalf("a failing payload lookup blocked the request: %v", err)
	}
}

// NewAbuse returns a nil *Abuse when abuse checking is off. Storing that in an
// interface would give a non-nil interface holding a nil pointer, and every
// guard would sail past it into a nil dereference.
func TestNilAbuseStaysNil(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	if srv.a != nil {
		t.Fatal("a nil *Abuse became a non-nil interface")
	}
}

// waitFor polls cond briefly. The fingerprint is persisted off the request
// path, so a test that asserted immediately would be racing the write.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
