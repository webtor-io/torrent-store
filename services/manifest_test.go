package services

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"google.golang.org/protobuf/proto"

	pb "github.com/webtor-io/torrent-store/proto"
)

func makeMultiFileTorrent(t *testing.T, name string, files []metainfo.FileInfo) []byte {
	t.Helper()
	info := metainfo.Info{
		Name:        name,
		PieceLength: 1024,
		Pieces:      make([]byte, 20),
		Files:       files,
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("info marshal: %v", err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes, CreatedBy: "test"}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func TestBuildManifest(t *testing.T) {
	torrent := makeMultiFileTorrent(t, "show", []metainfo.FileInfo{
		{Path: []string{"s01", "e01.mkv"}, Length: 100},
		{Path: []string{"s01", "e02.mkv"}, Length: 200},
	})
	reply, err := buildManifest(torrent)
	if err != nil {
		t.Fatal(err)
	}
	if reply.GetName() != "show" {
		t.Fatalf("name = %q, want show", reply.GetName())
	}
	if len(reply.GetFiles()) != 2 {
		t.Fatalf("files = %d, want 2", len(reply.GetFiles()))
	}
	// Paths must be name-prefixed (matches rest-api convention).
	want := []string{"show", "s01", "e01.mkv"}
	got := reply.GetFiles()[0].GetPath()
	if len(got) != len(want) {
		t.Fatalf("path = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path = %v, want %v", got, want)
		}
	}
	if reply.GetFiles()[1].GetLength() != 200 {
		t.Fatalf("length = %d, want 200", reply.GetFiles()[1].GetLength())
	}
}

// fakeProvider is an in-memory StoreProvider for exercising the multi-level
// manifest cache logic. supportsMani=false mimics a tier that opts out of
// manifest caching (PushManifest no-ops, PullManifest always misses).
type fakeProvider struct {
	name          string
	supportsMani  bool
	mu            sync.Mutex
	torrents      map[string][]byte
	manifests     map[string][]byte
	pullManiCalls int
	pushManiCalls int
}

func newFakeProvider(name string, supportsMani bool) *fakeProvider {
	return &fakeProvider{
		name:         name,
		supportsMani: supportsMani,
		torrents:     map[string][]byte{},
		manifests:    map[string][]byte{},
	}
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Push(_ context.Context, h string, torrent []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents[h] = torrent
	return true, nil
}

func (f *fakeProvider) Pull(_ context.Context, h string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.torrents[h]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (f *fakeProvider) Touch(_ context.Context, h string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.torrents[h]; !ok {
		return false, ErrNotFound
	}
	return true, nil
}

func (f *fakeProvider) PushManifest(_ context.Context, h string, manifest []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushManiCalls++
	if !f.supportsMani {
		return true, nil // S3-like no-op
	}
	f.manifests[h] = manifest
	return true, nil
}

func (f *fakeProvider) PullManifest(_ context.Context, h string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullManiCalls++
	if !f.supportsMani {
		return nil, ErrNotFound
	}
	v, ok := f.manifests[h]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func TestStoreManifestBuildOnceAndBackfill(t *testing.T) {
	fast := newFakeProvider("fast", true)
	slow := newFakeProvider("slow", true)
	s3 := newFakeProvider("s3", false)
	store := NewStore([]StoreProvider{fast, slow, s3})

	torrent := makeMultiFileTorrent(t, "x", []metainfo.FileInfo{
		{Path: []string{"a"}, Length: 1},
	})
	const h = "deadbeef"
	_, _ = fast.Push(context.Background(), h, torrent)
	_, _ = slow.Push(context.Background(), h, torrent)

	var builds int
	build := func(b []byte) ([]byte, error) {
		builds++
		r, err := buildManifest(b)
		if err != nil {
			return nil, err
		}
		return proto.Marshal(r)
	}

	out, err := store.Manifest(context.Background(), h, build)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	// Manifest must be cached in both manifest-capable tiers, not in S3.
	if len(fast.manifests[h]) == 0 || len(slow.manifests[h]) == 0 {
		t.Fatal("manifest not cached in fast/slow tiers")
	}
	if len(s3.manifests) != 0 {
		t.Fatal("s3 must not cache manifests")
	}
	reply := &pb.FilesReply{}
	if err := proto.Unmarshal(out, reply); err != nil || reply.GetName() != "x" {
		t.Fatalf("bad manifest payload: %v", err)
	}
}

func TestStorePullManifestBackfillsUpperTier(t *testing.T) {
	fast := newFakeProvider("fast", true)
	slow := newFakeProvider("slow", true)
	store := NewStore([]StoreProvider{fast, slow})

	const h = "abc123"
	// Seed only the slow tier.
	_, _ = slow.PushManifest(context.Background(), h, []byte("payload"))

	out, err := store.pullManifest(context.Background(), h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "payload" {
		t.Fatalf("payload = %q", out)
	}
	// Fast tier should have been backfilled.
	if string(fast.manifests[h]) != "payload" {
		t.Fatal("fast tier not backfilled")
	}
}

func TestStoreManifestServesFromCacheWithoutBuild(t *testing.T) {
	fast := newFakeProvider("fast", true)
	store := NewStore([]StoreProvider{fast})

	const h = "cached1"
	reply := &pb.FilesReply{Name: "pre"}
	payload, _ := proto.Marshal(reply)
	_, _ = fast.PushManifest(context.Background(), h, payload)

	var builds int
	out, err := store.Manifest(context.Background(), h, func(_ []byte) ([]byte, error) {
		builds++
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 0 {
		t.Fatalf("builds = %d, want 0 (cache hit)", builds)
	}
	got := &pb.FilesReply{}
	if err := proto.Unmarshal(out, got); err != nil || got.GetName() != "pre" {
		t.Fatalf("bad cached payload: %v", err)
	}
}
