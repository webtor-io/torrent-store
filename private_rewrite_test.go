package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	s "github.com/webtor-io/torrent-store/services"
)

const (
	fakePasskey = "0123456789abcdef0123456789abcdef"
	fakeTracker = "https://tracker.invalid/" + fakePasskey + "/announce"
	fakeSeed    = "https://seed.tracker.invalid/" + fakePasskey + "/"
)

// fakeBucket lists every key it holds, private/ ones included, in key order
// and in pages of page keys: the root-only listing is S3.ListRoot's job
// (TestS3ListRootStreamsRootOnly), so here the tool's own filter must skip them.
type fakeBucket struct {
	mu         sync.Mutex
	objs       map[string][]byte
	page       int
	gets, puts []string
	getErr     map[string]error
	putErr     error
	versErr    error
	onGet      func(key string)
}

func (b *fakeBucket) ListRoot(_ context.Context, startAfter string, page func([]string) bool) error {
	b.mu.Lock()
	var keys []string
	for k := range b.objs {
		if k > startAfter {
			keys = append(keys, k)
		}
	}
	b.mu.Unlock()
	sort.Strings(keys)
	for len(keys) > 0 {
		n := min(b.page, len(keys))
		if !page(keys[:n]) {
			return nil
		}
		keys = keys[n:]
	}
	return nil
}

func (b *fakeBucket) GetObject(_ context.Context, key string) ([]byte, error) {
	if b.onGet != nil {
		b.onGet(key)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets = append(b.gets, key)
	if err := b.getErr[key]; err != nil {
		return nil, err
	}
	raw, ok := b.objs[key]
	if !ok {
		return nil, s.ErrNotFound
	}
	return raw, nil
}

func (b *fakeBucket) PutObject(_ context.Context, key string, body []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts = append(b.puts, key)
	if b.putErr != nil {
		return b.putErr
	}
	b.objs[key] = body
	return nil
}

func (b *fakeBucket) Versioning(context.Context) (string, error) {
	if b.versErr != nil {
		return "", b.versErr
	}
	return "Enabled", nil
}

type fakeCache struct {
	mu              sync.Mutex
	objs            map[string][]byte
	dels            []string
	pullErr, delErr error
}

func (c *fakeCache) Pull(_ context.Context, h string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pullErr != nil {
		return nil, c.pullErr
	}
	raw, ok := c.objs[h]
	if !ok {
		return nil, s.ErrNotFound
	}
	return raw, nil
}

func (c *fakeCache) Delete(_ context.Context, h string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.delErr != nil {
		return c.delErr
	}
	c.dels = append(c.dels, h)
	delete(c.objs, h)
	return nil
}

// mkTorrent returns a .torrent named name and its infohash.
func mkTorrent(t *testing.T, name string, private bool, announce string, list metainfo.AnnounceList, urls metainfo.UrlList) (string, []byte) {
	t.Helper()
	info := metainfo.Info{Name: name, Length: 1024, PieceLength: 1024, Pieces: make([]byte, 20)}
	if private {
		info.Private = &private
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: ib, Announce: announce, AnnounceList: list, UrlList: urls, Comment: "kept", CreationDate: 1000}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return mi.HashInfoBytes().HexString(), buf.Bytes()
}

type fixture struct {
	bucket *fakeBucket
	cache  *fakeCache
	orig   map[string][]byte
	// hashes by role
	public, infoOnly, garbage string
	withLists                 []string // announce, announce-list, url-list
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{bucket: &fakeBucket{objs: map[string][]byte{}, page: 3}, cache: &fakeCache{objs: map[string][]byte{}}}
	put := func(h string, raw []byte) string { f.bucket.objs[h] = raw; return h }
	f.public = put(mkTorrent(t, "public", false, fakeTracker, nil, metainfo.UrlList{fakeSeed}))
	f.infoOnly = put(mkTorrent(t, "info-only", true, "", nil, nil))
	f.withLists = []string{
		put(mkTorrent(t, "announce", true, fakeTracker, nil, nil)),
		put(mkTorrent(t, "announce-list", true, "", metainfo.AnnounceList{{fakeTracker}}, nil)),
		put(mkTorrent(t, "url-list", true, "", nil, metainfo.UrlList{fakeSeed})),
	}
	f.garbage = put(strings.Repeat("f", 40), []byte("not bencode "+fakeTracker))
	for _, k := range []string{f.infoOnly + ".manifest", f.infoOnly + ".fingerprint", "private/u1/" + f.withLists[0], "README"} {
		f.bucket.objs[k] = []byte(fakeTracker)
	}
	// Redis holds the first uploader's copy of one hash and an info-only copy of another.
	f.cache.objs[f.withLists[0]] = f.bucket.objs[f.withLists[0]]
	f.cache.objs[f.infoOnly] = f.bucket.objs[f.infoOnly]
	f.orig = map[string][]byte{}
	for k, v := range f.bucket.objs {
		f.orig[k] = v
	}
	return f
}

func (f *fixture) run(t *testing.T, ctx context.Context, o rewriteOptions) (rewriteReport, string, error) {
	t.Helper()
	var log bytes.Buffer
	if o.concurrency == 0 {
		o.concurrency = 2
	}
	w := &rewriter{b: f.bucket, c: f.cache, o: o, log: &log}
	err := w.run(ctx)
	out, jerr := json.Marshal(w.r)
	if jerr != nil {
		t.Fatal(jerr)
	}
	all := string(out) + log.String()
	for _, secret := range []string{"tracker.invalid", fakePasskey, "not bencode"} {
		if strings.Contains(all, secret) {
			t.Fatalf("output carries %q:\n%s", secret, all)
		}
	}
	return w.r, all, err
}

func trackersOf(t *testing.T, raw []byte) (string, []string) {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	urls := append(mi.UpvertedAnnounceList().DistinctValues(), mi.UrlList...)
	return mi.HashInfoBytes().HexString(), urls
}

func TestPrivateRewriteDryRun(t *testing.T) {
	f := newFixture(t)
	r, _, err := f.run(t, t.Context(), rewriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := rewriteReport{Mode: "dry-run", Versioning: "Enabled", Redis: true, Torrents: 6, Other: 4,
		Public: 1, PrivateInfoOnly: 1, PrivateWithLists: 3, ParseErrors: 1, RedisPresent: 2, RedisWithLists: 1,
		LastKey: "private/u1/" + f.withLists[0]}
	if r != want {
		t.Fatalf("report\n got %+v\nwant %+v", r, want)
	}
	if len(f.bucket.puts) != 0 || len(f.cache.dels) != 0 {
		t.Fatalf("dry run wrote: puts=%q dels=%q", f.bucket.puts, f.cache.dels)
	}
	for _, k := range f.bucket.gets {
		if !hashKey.MatchString(k) {
			t.Fatalf("fetched %q, which is not a torrent object", k)
		}
	}
	for k, v := range f.orig {
		if !bytes.Equal(f.bucket.objs[k], v) {
			t.Fatalf("%s changed in a dry run", k)
		}
	}
}

func TestPrivateRewriteWrite(t *testing.T) {
	f := newFixture(t)
	r, _, err := f.run(t, t.Context(), rewriteOptions{write: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rewritten != 3 || r.WriteErrors != 0 || r.RedisDeleted != 1 || r.Mode != "write" {
		t.Fatalf("report %+v", r)
	}
	if got := slices.Sorted(slices.Values(f.bucket.puts)); !slices.Equal(got, slices.Sorted(slices.Values(f.withLists))) {
		t.Fatalf("puts = %q, want exactly the three private copies with lists", got)
	}
	for _, h := range f.withLists {
		hash, urls := trackersOf(t, f.bucket.objs[h])
		if hash != h || len(urls) != 0 {
			t.Fatalf("%s rewritten to infohash %s with trackers %q", h, hash, urls)
		}
		if bytes.Contains(f.bucket.objs[h], []byte("kept")) == false {
			t.Fatalf("%s lost its comment", h)
		}
	}
	for _, h := range []string{f.public, f.infoOnly, f.garbage} {
		if !bytes.Equal(f.bucket.objs[h], f.orig[h]) {
			t.Fatalf("%s changed", h)
		}
	}
	if !slices.Equal(f.cache.dels, []string{f.withLists[0]}) || f.cache.objs[f.infoOnly] == nil {
		t.Fatalf("redis dels = %q; the info-only copy must stay", f.cache.dels)
	}

	// The verification dry run after a write finds nothing left.
	r, _, err = f.run(t, t.Context(), rewriteOptions{})
	if err != nil || r.PrivateWithLists != 0 || r.RedisWithLists != 0 || r.PrivateInfoOnly != 4 {
		t.Fatalf("second dry run: %+v, %v", r, err)
	}
}

// A Redis copy with lists over an info-only S3 object (a DEL that failed on an
// earlier run) is found and dropped too.
func TestPrivateRewriteStaleRedisOverCleanS3(t *testing.T) {
	f := newFixture(t)
	_, stale := mkTorrent(t, "info-only", true, fakeTracker, nil, nil)
	f.cache.objs[f.infoOnly] = stale
	r, _, err := f.run(t, t.Context(), rewriteOptions{write: true})
	if err != nil || r.RedisWithLists != 2 || !slices.Contains(f.cache.dels, f.infoOnly) {
		t.Fatalf("report %+v, dels %q, err %v", r, f.cache.dels, err)
	}
}

func TestPrivateRewriteHashMismatchAndErrors(t *testing.T) {
	f := newFixture(t)
	wrong := strings.Repeat("a", 40)
	f.bucket.objs[wrong] = f.bucket.objs[f.withLists[0]]
	f.bucket.getErr = map[string]error{f.withLists[1]: errors.New("boom " + fakeTracker)}
	f.bucket.putErr = errors.New("put " + fakeTracker)
	r, log, err := f.run(t, t.Context(), rewriteOptions{write: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.HashMismatch != 1 || r.GetErrors != 1 || r.WriteErrors != 2 || r.Rewritten != 0 {
		t.Fatalf("report %+v", r)
	}
	if slices.Contains(f.bucket.puts, wrong) {
		t.Fatal("a copy whose infohash is not its key was written")
	}
	// A failed S3 write leaves Redis alone: the S3 copy still has the lists.
	if len(f.cache.dels) != 0 {
		t.Fatalf("redis dels after failed writes: %q", f.cache.dels)
	}
	if !strings.Contains(log, wrong+"\thash\tmismatch") || !strings.Contains(log, f.withLists[1]+"\tget\t") {
		t.Fatalf("stderr lacks key and class:\n%s", log)
	}
}

// A failed Redis DEL is an error, not a deletion; a failed Redis read and an
// unreadable versioning status are reported, never guessed.
func TestPrivateRewriteRedisAndVersioningErrors(t *testing.T) {
	f := newFixture(t)
	f.cache.delErr = errors.New("del " + fakeTracker)
	f.bucket.versErr = errors.New("denied")
	r, log, err := f.run(t, t.Context(), rewriteOptions{write: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.RedisDeleted != 0 || r.RedisErrors != 1 || r.Versioning != "unknown" || r.Rewritten != 3 {
		t.Fatalf("report %+v", r)
	}
	if !strings.Contains(log, f.withLists[0]+"\tredis-del\t") {
		t.Fatalf("stderr lacks the failed DEL:\n%s", log)
	}
	g := newFixture(t)
	g.cache.pullErr = errors.New("get " + fakeTracker)
	r, _, err = g.run(t, t.Context(), rewriteOptions{})
	if err != nil || r.RedisErrors == 0 || r.RedisPresent != 0 {
		t.Fatalf("report %+v, err %v", r, err)
	}
}

func TestPrivateRewriteLimitAndStartAfter(t *testing.T) {
	f := newFixture(t)
	var all []string
	for k := range f.bucket.objs {
		if hashKey.MatchString(k) {
			all = append(all, k)
		}
	}
	sort.Strings(all)

	r, _, err := f.run(t, t.Context(), rewriteOptions{limit: 4})
	if err != nil || r.Torrents != 4 || r.LastKey < all[3] || r.LastKey >= all[4] {
		t.Fatalf("limit 4: %+v, %v", r, err)
	}
	if got := slices.Sorted(slices.Values(f.bucket.gets)); !slices.Equal(got, all[:4]) {
		t.Fatalf("limit 4 fetched %q, want %q", got, all[:4])
	}

	f.bucket.gets = nil
	r, _, err = f.run(t, t.Context(), rewriteOptions{startAfter: r.LastKey})
	if err != nil || r.Torrents != 2 {
		t.Fatalf("resume: %+v, %v", r, err)
	}
	if got := slices.Sorted(slices.Values(f.bucket.gets)); !slices.Equal(got, all[4:]) {
		t.Fatalf("resume fetched %q, want %q", got, all[4:])
	}
}

func TestPrivateRewriteListOnly(t *testing.T) {
	f := newFixture(t)
	r, _, err := f.run(t, t.Context(), rewriteOptions{listOnly: true})
	if err != nil || r.Torrents != 6 || r.Other != 4 || r.Mode != "list-only" || len(f.bucket.gets) != 0 {
		t.Fatalf("%+v, gets %q, %v", r, f.bucket.gets, err)
	}
}

// Interrupted mid-page, last_key stays at the end of the last page that
// finished, so --start-after re-reads the cut page instead of skipping it.
func TestPrivateRewriteInterruptKeepsResumePoint(t *testing.T) {
	b := &fakeBucket{objs: map[string][]byte{}, page: 2}
	var keys []string
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		h, raw := mkTorrent(t, n, false, "", nil, nil)
		b.objs[h] = raw
		keys = append(keys, h)
	}
	sort.Strings(keys)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.onGet = func(k string) {
		if k == keys[2] {
			cancel()
		}
	}
	f := &fixture{bucket: b, cache: &fakeCache{objs: map[string][]byte{}}}
	r, _, err := f.run(t, ctx, rewriteOptions{concurrency: 1})
	if !errors.Is(err, context.Canceled) || !r.Interrupted || r.LastKey != keys[1] {
		t.Fatalf("%+v, %v; want last_key %s", r, err, keys[1])
	}
}
