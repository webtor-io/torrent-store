package services

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	pb "github.com/webtor-io/torrent-store/proto"
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
