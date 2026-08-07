package services

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
	if len(fast.fingerprints[h]) == 0 || len(slow.fingerprints[h]) == 0 {
		t.Fatal("fingerprint not cached in the opted-in tiers")
	}
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
	_, _ = slow.PushFingerprint(context.Background(), h, payload)

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
	if len(first.GetFingerprints()) == 0 {
		t.Fatal("no fingerprints returned")
	}
	fp := first.GetFingerprints()[0]
	if fp.GetValue() == "" || fp.GetLength() == 0 {
		t.Fatalf("malformed fingerprint in reply: %+v", fp)
	}
	if len(p.fingerprints[h]) == 0 {
		t.Fatal("derived fingerprint was not cached")
	}

	// A second call must not need the torrent at all: drop it and check the
	// reply still comes back identical, proving it is served from the cache.
	delete(p.torrents, h)
	second, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: h})
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if len(second.GetFingerprints()) != len(first.GetFingerprints()) ||
		second.GetFingerprints()[0].GetValue() != fp.GetValue() {
		t.Fatalf("cached reply differs: %+v vs %+v", second.GetFingerprints(), first.GetFingerprints())
	}
}

func TestServerFingerprintUnknownHash(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	_, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: "nosuchhash"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v (err %v), want NotFound", status.Code(err), err)
	}
}

// The abuse lookup takes raw digests. Anything that cannot be one is dropped
// rather than sent as a query that could only ever miss.
func TestFingerprintDigestsRejectsUnusable(t *testing.T) {
	good := strings.Repeat("ab", 32) // 64 hex chars -> 32 bytes
	blob := []byte(strings.Join([]string{
		good + "\t1",
		"nothex!!" + strings.Repeat("0", 56) + "\t1",
		strings.Repeat("ab", 16) + "\t1", // valid hex, wrong length
		"v1layout:" + good + "\t1",       // legacy prefix still yields a digest
	}, "\n"))
	got := fingerprintDigests(blob)
	if len(got) != 2 {
		t.Fatalf("got %d digests, want 2: %x", len(got), got)
	}
	for _, d := range got {
		if len(d) != 32 {
			t.Fatalf("digest of wrong length survived: %x", d)
		}
	}
}
