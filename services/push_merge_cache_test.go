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

// failingPullProvider is a tier whose reads fail with an outage rather than
// a miss. Writes land, so a test sees whether Push wrote blind.
type failingPullProvider struct {
	*fakeProvider
}

func (f *failingPullProvider) Pull(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("down")
}

// A private push that cannot read the stored copy writes nothing and answers
// Unavailable. A public torrent still falls back to pushing as-is.
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

// Each pod reads through its own L1 first. A private push reads the copy
// every pod shares: a pod whose L1 already holds an info-only copy would find
// nothing to clear and leave an old copy with lists in the shared tier.
func TestPushPrivateReadsSharedTier(t *testing.T) {
	l1, shared := newFakeProvider("l1", true), newFakeProvider("shared", true)
	srv := NewServer(NewStore([]StoreProvider{l1, shared}), nil, nil, nil)
	bare := makeTorrent(t, "", nil, nil, true)
	h := infoHashOf(t, bare)
	l1.torrents[h] = bare
	shared.torrents[h] = makeTorrent(t, passkeyA, nil, nil, true) // stored by keep-first before records
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeTorrent(t, passkeyB, nil, nil, true)}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := storedTrackers(t, shared, h); len(got) != 0 {
		t.Fatalf("shared trackers = %v, want none", got)
	}
}
