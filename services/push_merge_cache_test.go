package services

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A re-push with new trackers inside pushm's cache window must still store the
// merged payload. Regression test for the cached Push result swallowing the
// write: Push #1 primed pushm, Push #2 merged the announce lists, and the
// lazymap handed back the cached `true` without ever writing the merged bytes.
func TestRepushWithNewTrackersStoresMergedPayload(t *testing.T) {
	p := newFakeProvider("mem", true)
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)

	first := makeTorrent(t, "http://a/announce", metainfo.AnnounceList{{"http://a/announce"}}, nil, false)
	second := makeTorrent(t, "http://a/announce", metainfo.AnnounceList{{"http://a/announce"}, {"udp://b:80/announce"}}, nil, false)

	r1, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: first})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: second}); err != nil {
		t.Fatalf("second push: %v", err)
	}

	stored, ok := p.torrents[r1.GetInfoHash()]
	if !ok {
		t.Fatal("nothing stored")
	}
	mi, err := metainfo.Load(bytes.NewReader(stored))
	if err != nil {
		t.Fatalf("stored bytes do not parse: %v", err)
	}
	trackers := mi.UpvertedAnnounceList().DistinctValues()
	if !slices.Contains(trackers, "udp://b:80/announce") {
		t.Fatalf("merged tracker was not persisted, stored announce list: %v", trackers)
	}
}

// openTracker stands in for DEFAULT_TRACKERS, which magnet2torrent leaves in
// the metainfo it resolves.
const openTracker = "udp://open.invalid:1337/announce"

func storedTrackers(t *testing.T, p *fakeProvider, h string) []string {
	t.Helper()
	stored, ok := p.torrents[h]
	if !ok {
		t.Fatal("nothing stored")
	}
	trackers, _ := parseAnnounce(t, stored)
	return trackers
}

// Two users push the same private torrent with their own passkeys: the store
// keeps the first user's and never shows the second's to anyone.
func TestPushPrivateKeepsFirstPasskey(t *testing.T) {
	p := newFakeProvider("mem", true)
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, []string{openTracker})

	r, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyA, nil, nil, true)})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, metainfo.AnnounceList{{passkeyB}}, nil, true)}); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if bytes.Contains(p.torrents[r.GetInfoHash()], []byte("BBB")) {
		t.Fatal("second user's passkey was stored")
	}
	if got := storedTrackers(t, p, r.GetInfoHash()); !slices.Equal(got, []string{passkeyA}) {
		t.Fatalf("stored trackers = %v, want only the first upload's", got)
	}
}

// The private rule must not leak into public torrents: their announce lists
// still union, which is what fixed magnet-only torrents stalling.
func TestPushPublicStillUnionsTrackers(t *testing.T) {
	p := newFakeProvider("mem", true)
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)

	r, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyA, nil, nil, false)})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, false)}); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if got := storedTrackers(t, p, r.GetInfoHash()); !slices.Equal(got, []string{passkeyA, passkeyB}) {
		t.Fatalf("stored trackers = %v, want both", got)
	}
}

// A private torrent resolved from a bare magnet arrives with only the default
// trackers magnet2torrent announced: it is stored trackerless, and the first
// real upload then supplies its trackers. The default can also arrive as
// announce (rest-api puts a magnet's first tr= there).
func TestPushPrivateDropsDefaultTrackers(t *testing.T) {
	p := newFakeProvider("mem", true)
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, []string{openTracker})

	r, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, openTracker, metainfo.AnnounceList{{openTracker}}, nil, true)})
	if err != nil {
		t.Fatalf("magnet push: %v", err)
	}
	if got := storedTrackers(t, p, r.GetInfoHash()); len(got) != 0 {
		t.Fatalf("stored trackers = %v, want none", got)
	}
	mi, err := metainfo.Load(bytes.NewReader(p.torrents[r.GetInfoHash()]))
	if err != nil {
		t.Fatal(err)
	}
	if mi.HashInfoBytes().HexString() != r.GetInfoHash() {
		t.Fatal("dropping trackers changed the infohash")
	}

	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, "", metainfo.AnnounceList{{passkeyA}, {openTracker}}, nil, true)}); err != nil {
		t.Fatalf("upload push: %v", err)
	}
	if got := storedTrackers(t, p, r.GetInfoHash()); !slices.Equal(got, []string{passkeyA}) {
		t.Fatalf("stored trackers = %v, want only the upload's own", got)
	}
}

// failingPullProvider is a tier whose reads fail with an outage rather than
// a miss. Writes land, so a test sees whether Push wrote blind.
type failingPullProvider struct {
	*fakeProvider
}

func (f *failingPullProvider) Pull(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("down")
}

// Without the stored copy Push cannot tell whether a private torrent already
// has its first trackers, so it refuses rather than overwrite them. A public
// torrent still falls back to pushing as-is.
func TestPushPrivatePullErrorIsUnavailable(t *testing.T) {
	p := &failingPullProvider{newFakeProvider("mem", true)}
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)

	_, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, true)})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("private push error = %v, want Unavailable", err)
	}
	if len(p.torrents) != 0 {
		t.Fatal("private torrent was written without reading the stored copy")
	}

	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, false)}); err != nil {
		t.Fatalf("public push: %v", err)
	}
	if len(p.torrents) != 1 {
		t.Fatal("public torrent was not pushed as-is")
	}
}

// Each pod reads through its own L1 first. A pod whose L1 still holds the
// trackerless copy must not adopt a second upload over the first one that
// another pod already stored.
func TestPushPrivateReadsSharedTier(t *testing.T) {
	shared := newFakeProvider("shared", true)
	pod1 := NewServer(NewStore([]StoreProvider{newFakeProvider("l1", true), shared}), nil, nil, nil)
	pod2 := NewServer(NewStore([]StoreProvider{newFakeProvider("l1", true), shared}), nil, nil, nil)

	r, err := pod1.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, "", nil, nil, true)})
	if err != nil {
		t.Fatalf("magnet push: %v", err)
	}
	if _, err := pod2.Pull(context.Background(), &pb.PullRequest{InfoHash: r.GetInfoHash()}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := pod1.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyA, nil, nil, true)}); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if _, err := pod2.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, true)}); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if got := storedTrackers(t, shared, r.GetInfoHash()); !slices.Equal(got, []string{passkeyA}) {
		t.Fatalf("shared trackers = %v, want the first upload's", got)
	}
}

// After repeated misses the rate limiter answers ErrNotFound without reading.
// A private push must not take that for "not stored" and write blind.
func TestPushPrivateIgnoresMissRateLimit(t *testing.T) {
	shared := newFakeProvider("shared", true)
	pod1 := NewServer(NewStore([]StoreProvider{shared}), nil, nil, nil)
	pod2 := NewServer(NewStore([]StoreProvider{shared}), nil, nil, nil)
	a := makeTorrent(t, passkeyA, nil, nil, true)
	pt, err := parseTorrent(a)
	if err != nil {
		t.Fatal(err)
	}
	h := pt.mi.HashInfoBytes().HexString()
	for i := 0; i < 10; i++ {
		_, _ = pod2.Touch(context.Background(), &pb.TouchRequest{InfoHash: h})
	}
	if _, err := pod1.Push(context.Background(), &pb.PushRequest{Torrent: a}); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if _, err := pod2.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, true)}); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if got := storedTrackers(t, shared, h); !slices.Equal(got, []string{passkeyA}) {
		t.Fatalf("shared trackers = %v, want the first upload's", got)
	}
}
