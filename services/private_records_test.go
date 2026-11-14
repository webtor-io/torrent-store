package services

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
)

const (
	uidA = "0123456789abcdef0123456789abcdef01234567" // acct deb87fab
	uidB = "89abcdef0123456789abcdef0123456789abcdef" // acct 5f44da3a
	uidC = "fedcba9876543210fedcba9876543210fedcba98"
	trA  = "http://tracker.example/a/00000000000000000000000000000000/announce"
	trA2 = "http://tracker.example/a2/00000000000000000000000000000000/announce"
	trB  = "udp://tracker.invalid:6969/00000000000000000000000000000000/announce"
)

var (
	ctx      = context.Background()
	testHash = privaterecord.Vector.Hash
)

// memObjects is RecordObjects in memory. ops logs "get|put|delete <key>" and
// "list <prefix>" in call order; failOn makes one op fail on keys with a prefix.
// A missing key answers exactly like *providers.S3: ErrNotFound; ListObjects
// returns keys sorted, as S3 does.
type memObjects struct {
	mu   sync.Mutex
	objs map[string][]byte
	ops  []string
	fail map[string]error // "op prefix" -> err
}

func newMemObjects() *memObjects {
	return &memObjects{objs: map[string][]byte{}, fail: map[string]error{}}
}

func (m *memObjects) failOn(op, prefix string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[op+" "+prefix] = err
}

// do logs one op and returns its injected failure; the caller holds mu.
func (m *memObjects) do(op, key string) error {
	m.ops = append(m.ops, op+" "+key)
	for k, err := range m.fail {
		if o, p, _ := strings.Cut(k, " "); o == op && strings.HasPrefix(key, p) {
			return err
		}
	}
	return nil
}

// count is the number of ops of that kind on keys with prefix.
func (m *memObjects) count(op, prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.ops {
		if o, k, _ := strings.Cut(s, " "); o == op && strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

func (m *memObjects) GetObject(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.do("get", key); err != nil {
		return nil, err
	}
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(b), nil
}

func (m *memObjects) PutObject(_ context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.do("put", key); err != nil {
		return err
	}
	m.objs[key] = bytes.Clone(body)
	return nil
}

func (m *memObjects) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.do("delete", key); err != nil {
		return err
	}
	delete(m.objs, key)
	return nil
}

func (m *memObjects) ListObjects(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.do("list", prefix); err != nil {
		return nil, err
	}
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// record decodes private/<uid>/<h>; it fails the test if the object is absent.
func (m *memObjects) record(t *testing.T, h, uid string) record {
	t.Helper()
	m.mu.Lock()
	b, ok := m.objs[recordKey(uid, h)]
	m.mu.Unlock()
	if !ok {
		t.Fatalf("no object %s", recordKey(uid, h))
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func newRecords(obj RecordObjects) *PrivateRecords {
	return NewPrivateRecords(obj, [][]byte{privaterecord.Vector.Key})
}

// seed writes a v1 record JSON around blob without logging an op.
func seed(t *testing.T, obj *memObjects, h, uid string, blob []byte) {
	t.Helper()
	now := time.Now().UTC()
	b, err := json.Marshal(record{V: 1, TiersEnc: blob, Source: "site", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	obj.mu.Lock()
	obj.objs[recordKey(uid, h)] = b
	obj.mu.Unlock()
}

func TestUpsertOutcomes(t *testing.T) {
	obj := newMemObjects()
	r := newRecords(obj)
	up := func(tiers [][]string, source string, write bool) string {
		t.Helper()
		o, err := r.upsert(ctx, testHash, uidA, tiers, source, write)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	a, a2 := [][]string{{trA}}, [][]string{{trA2}}
	if o := up(a, "site", false); o != "inserted" || obj.count("put", "") != 0 {
		t.Fatalf("dry insert = %s, puts %d", o, obj.count("put", ""))
	}
	if o := up(a, "site", true); o != "inserted" {
		t.Fatalf("insert = %s", o)
	}
	first := obj.record(t, testHash, uidA)
	if o := up(a, "site", true); o != "unchanged" || obj.count("put", "") != 1 {
		t.Fatalf("same trackers, fresh nonce: %s, puts %d", o, obj.count("put", ""))
	}
	if o := up(a, "vault", true); o != "unchanged" || obj.record(t, testHash, uidA).Source != "vault" || obj.count("put", "") != 2 {
		t.Fatalf("same trackers, other source: %s, %+v", o, obj.record(t, testHash, uidA))
	}
	if o := up(a2, "site", false); o != "updated" || obj.count("put", "") != 2 {
		t.Fatalf("dry update = %s", o)
	}
	if o := up(a2, "site", true); o != "updated" {
		t.Fatalf("update = %s", o)
	}
	got := obj.record(t, testHash, uidA)
	if !got.CreatedAt.Equal(first.CreatedAt) || got.UpdatedAt.Before(first.UpdatedAt) || !reflect.DeepEqual(got.Hosts, []string{"tracker.example"}) {
		t.Fatalf("updated record = %+v, first %+v", got, first)
	}
	if _, tiers, err := r.open(ctx, testHash, uidA); err != nil || !reflect.DeepEqual(tiers, a2) {
		t.Fatalf("open after update = %q, %v", tiers, err)
	}
	if keys := slices.Collect(maps.Keys(obj.objs)); !reflect.DeepEqual(keys, []string{"private/" + uidA + "/" + testHash}) {
		t.Fatalf("objects = %q, want the one record and no index", keys)
	}
}

// A record no key opens (a rotation without the previous key) is not a dead end:
// its owner's next upload replaces it. An S3 error is not "no record".
func TestUpsertOverwritesUnreadable(t *testing.T) {
	foreign, _ := privaterecord.Seal(bytes.Repeat([]byte{1}, 32), testHash, uidA, [][]string{{trB}})
	for name, setup := range map[string]func(*memObjects){
		"other key": func(o *memObjects) { seed(t, o, testHash, uidA, foreign) },
		"not json":  func(o *memObjects) { o.objs[recordKey(uidA, testHash)] = []byte("{") },
	} {
		obj := newMemObjects()
		setup(obj)
		var was record
		_ = json.Unmarshal(obj.objs[recordKey(uidA, testHash)], &was) // zero when not JSON
		r := newRecords(obj)
		if o, err := r.upsert(ctx, testHash, uidA, [][]string{{trA}}, "site", false); err != nil || o != "updated" || obj.count("put", "") != 0 {
			t.Fatalf("%s, dry: %s, %v, puts %d", name, o, err, obj.count("put", ""))
		}
		if o, err := r.upsert(ctx, testHash, uidA, [][]string{{trA}}, "site", true); err != nil || o != "updated" {
			t.Fatalf("%s: %s, %v; want updated", name, o, err)
		}
		// created_at survives when the JSON decoded, else it is the write time.
		got := obj.record(t, testHash, uidA)
		if want := cmp.Or(was.CreatedAt, got.UpdatedAt); !got.CreatedAt.Equal(want) {
			t.Fatalf("%s: created_at %v, want %v", name, got.CreatedAt, want)
		}
		if _, tiers, err := r.open(ctx, testHash, uidA); err != nil || tiers[0][0] != trA {
			t.Fatalf("%s: open after overwrite = %q, %v", name, tiers, err)
		}
	}
	obj := newMemObjects()
	obj.failOn("get", "private/", errors.New("down"))
	if _, err := newRecords(obj).upsert(ctx, testHash, uidA, [][]string{{trA}}, "site", true); err == nil || obj.count("put", "") != 0 {
		t.Fatalf("S3 error on read: %v, puts %d; want an error and no write", err, obj.count("put", ""))
	}
}

func TestOpenRecord(t *testing.T) {
	other, _ := privaterecord.Seal(privaterecord.Vector.Key, strings.Repeat("ab", 20), uidA, [][]string{{trA}})
	foreign, _ := privaterecord.Seal(bytes.Repeat([]byte{1}, 32), testHash, uidA, [][]string{{trA}})
	for name, c := range map[string]struct {
		setup func(obj *memObjects)
		want  error
	}{
		"absent":            {func(*memObjects) {}, ErrNotFound},
		"sealed for a hash": {func(o *memObjects) { seed(t, o, testHash, uidA, other) }, errUnreadable},
		"other key":         {func(o *memObjects) { seed(t, o, testHash, uidA, foreign) }, errUnreadable},
		"truncated":         {func(o *memObjects) { seed(t, o, testHash, uidA, other[:20]) }, errUnreadable},
		"not json":          {func(o *memObjects) { o.objs[recordKey(uidA, testHash)] = []byte("{") }, errUnreadable},
	} {
		obj := newMemObjects()
		c.setup(obj)
		if _, _, err := newRecords(obj).open(ctx, testHash, uidA); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", name, err, c.want)
		}
	}
	boom := errors.New("down")
	obj := newMemObjects()
	obj.failOn("get", "private/", boom)
	if _, _, err := newRecords(obj).open(ctx, testHash, uidA); !errors.Is(err, boom) || errors.Is(err, ErrNotFound) {
		t.Fatalf("S3 error = %v, must stay an S3 error", err)
	}
	obj = newMemObjects()
	seed(t, obj, privaterecord.Vector.Hash, privaterecord.Vector.UID, privaterecord.Vector.Blob) // the vector, as the server sees it
	if rec, tiers, err := newRecords(obj).open(ctx, privaterecord.Vector.Hash, privaterecord.Vector.UID); err != nil || !reflect.DeepEqual(tiers, privaterecord.Vector.Tiers) || rec == nil {
		t.Fatalf("vector: %q, %v", tiers, err)
	}
}

func TestNewPrivateRecordsState(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	keys := [][]byte{privaterecord.Vector.Key}
	for _, c := range []struct {
		obj  RecordObjects
		keys [][]byte
		on   bool
		msg  string
	}{
		{newMemObjects(), keys, true, "private torrents: on"},
		{newMemObjects(), nil, false, "private torrents: off, PRIVATE_TRACKERS_KEY is not set"},
		{nil, keys, false, "private torrents: off, USE_S3 is not set"},
	} {
		hook.Reset()
		r := NewPrivateRecords(c.obj, c.keys)
		if r.Available() != c.on || hook.LastEntry() == nil || hook.LastEntry().Message != c.msg || hook.LastEntry().Level != logrus.InfoLevel {
			t.Fatalf("Available=%v, last log %v; want %v, %q", r.Available(), hook.LastEntry(), c.on, c.msg)
		}
	}
	if (*PrivateRecords)(nil).Available() {
		t.Fatal("nil records report available")
	}
}
