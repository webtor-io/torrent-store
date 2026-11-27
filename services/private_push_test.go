package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// privateServer: one fake tier, DEFAULT_TRACKERS = [openTracker], records in obj.
func privateServer(t *testing.T) (*Server, *fakeProvider, *memObjects) {
	t.Helper()
	p, obj := newFakeProvider("mem", true), newMemObjects()
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, []string{openTracker})
	srv.SetPrivate(newRecords(obj))
	return srv, p, obj
}

// pushAs pushes raw with Caller c and Source "site"; mod may change the request.
func pushAs(srv *Server, raw []byte, c *pb.Caller, mod func(*pb.PushRequest)) (*pb.PushReply, error) {
	r := &pb.PushRequest{Torrent: raw, Caller: c, Source: "site"}
	if mod != nil {
		mod(r)
	}
	return srv.Push(ctx, r)
}

// privateTorrent is a private torrent with these announce tiers; every one has
// the same info dict, so the same infohash.
func privateTorrent(t *testing.T, tiers ...[]string) []byte {
	t.Helper()
	return makeTorrent(t, "", tiers, nil, true)
}

// urlTiers is n tiers of one distinct URL each on tracker.example.
func urlTiers(n int) [][]string {
	out := make([][]string, n)
	for i := range out {
		out[i] = []string{fmt.Sprintf("http://tracker.example/%d/00000000000000000000000000000000/announce", i)}
	}
	return out
}

func dry(r *pb.PushRequest) { r.DryRun = true }

func TestPushWithCallerRefusals(t *testing.T) {
	one := privateTorrent(t, []string{trA})
	stopped := func() []byte { // private, refused by noExceptFixture
		i, yes := validInfo(), true
		i.Name, i.Private = "foo bar", &yes
		return makeRawTorrent(t, i)
	}()
	for _, c := range []struct {
		name   string
		off    bool // no S3 behind the records
		sl     bool // noExceptFixture stoplist
		raw    []byte
		caller *pb.Caller
		mod    func(*pb.PushRequest)
		code   codes.Code
		reason string
	}{
		{"no S3 or key", true, false, one, &pb.Caller{Uid: uidA}, nil, codes.FailedPrecondition, "private_unavailable"},
		{"no uid", false, false, one, &pb.Caller{}, nil, codes.PermissionDenied, "private_needs_account"},
		{"internal is not an account", false, false, one, &pb.Caller{Internal: true}, nil, codes.PermissionDenied, "private_needs_account"},
		{"no tracker", false, false, privateTorrent(t), &pb.Caller{Uid: uidA}, nil, codes.InvalidArgument, "private_no_tracker"},
		{"only DEFAULT_TRACKERS", false, false, privateTorrent(t, []string{openTracker}), &pb.Caller{Uid: uidA}, nil, codes.InvalidArgument, "private_no_tracker"},
		{"33 URLs", false, false, privateTorrent(t, urlTiers(33)...), &pb.Caller{Uid: uidA}, nil, codes.InvalidArgument, "private_too_many_trackers"},
		{"malformed uid", false, false, one, &pb.Caller{Uid: strings.ToUpper(uidA)}, nil, codes.InvalidArgument, ""},
		{"unknown source", false, false, one, &pb.Caller{Uid: uidA}, func(r *pb.PushRequest) { r.Source = "ftp" }, codes.InvalidArgument, ""},
		{"stoplist", false, true, stopped, &pb.Caller{Uid: uidA}, nil, codes.PermissionDenied, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, obj := newFakeProvider("mem", true), newMemObjects()
			var sl *Stoplist
			if c.sl {
				sl = mustStoplist(t, noExceptFixture)
			}
			srv := NewServer(NewStore([]StoreProvider{p}), nil, sl, []string{openTracker})
			records := newRecords(obj)
			if c.off {
				records = NewPrivateRecords(nil, [][]byte{privaterecord.Vector.Key})
			}
			srv.SetPrivate(records)
			_, err := pushAs(srv, c.raw, c.caller, c.mod)
			wantRefusal(t, err, c.code, c.reason)
			if len(p.torrents) != 0 || len(obj.objs) != 0 {
				t.Fatalf("refused push wrote: torrents %d, objects %d", len(p.torrents), len(obj.objs))
			}
		})
	}
}

func TestPushWithCallerStripsEveryTier(t *testing.T) {
	tiers := []*fakeProvider{newFakeProvider("l1", true), newFakeProvider("l2", true), newFakeProvider("s3", true)}
	srv := NewServer(NewStore([]StoreProvider{tiers[0], tiers[1], tiers[2]}), nil, nil, []string{openTracker})
	obj := newMemObjects()
	srv.SetPrivate(newRecords(obj))
	raw := privateTorrent(t, []string{trA, openTracker}, []string{trA2})
	h := infoHashOf(t, raw)
	before := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped))
	r, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil)
	if err != nil || r.GetRecordOutcome() != "inserted" {
		t.Fatalf("push = %v, %v", r, err)
	}
	for _, p := range tiers {
		if announce, webseeds := parseAnnounce(t, p.torrents[h]); len(announce)+len(webseeds) != 0 || bytes.Contains(p.torrents[h], []byte("00000000000000000000000000000000")) {
			t.Fatalf("%s stored announce lists: %v", p.name, announce)
		}
	}
	if _, got, err := srv.p.open(ctx, h, uidA); err != nil || !reflect.DeepEqual(got, [][]string{{trA}, {trA2}}) {
		t.Fatalf("record tiers = %q, %v; want [[trA] [trA2]] without the default tracker", got, err)
	}
	if rec := obj.record(t, h, uidA); bytes.Contains(rec.TiersEnc, []byte("tracker.example")) || rec.Source != "site" {
		t.Fatalf("record = %+v", rec)
	}
	if d := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped)) - before; d != 1 {
		t.Fatalf("stripped delta = %v", d)
	}
}

func TestPushSecondUploaderGetsOwnRecord(t *testing.T) {
	srv, _, obj := privateServer(t)
	a, b := privateTorrent(t, []string{trA}), privateTorrent(t, []string{trB})
	h := infoHashOf(t, a)
	for _, step := range []struct {
		raw  []byte
		uid  string
		want string
	}{{a, uidA, "inserted"}, {b, uidB, "inserted"}, {a, uidA, "unchanged"}} {
		if r, err := pushAs(srv, step.raw, &pb.Caller{Uid: step.uid}, nil); err != nil || r.GetRecordOutcome() != step.want {
			t.Fatalf("push %s = %v, %v; want %s", step.uid, r, err, step.want)
		}
	}
	if _, got, err := srv.p.open(ctx, h, uidB); err != nil || got[0][0] != trB {
		t.Fatalf("second uploader's record = %q, %v", got, err)
	}
	if obj.count("put", "private/") != 2 {
		t.Fatalf("record puts = %d, want 2 (the repeat is unchanged)", obj.count("put", "private/"))
	}
}

// Every write of a private hash is info-only and heals the copy it finds:
// whatever a push carries and whatever is stored, no tier names a tracker or
// webseed afterwards. A copy stored by keep-first before records (the first
// uploader's lists) is cleared by the next push of its hash, of any kind.
func TestPrivatePushLeavesNoLists(t *testing.T) {
	seedURL := metainfo.UrlList{"http://seed.example/00000000000000000000000000000000/"}
	stored := []struct {
		name string
		raw  []byte
	}{
		{"nothing", nil},
		{"info-only", privateTorrent(t)},
		{"first uploader's", makeTorrent(t, trA, metainfo.AnnounceList{{trA}, {trA2}}, nil, true)},
		{"webseed", makeTorrent(t, "", nil, seedURL, true)},
		{"defaults", privateTorrent(t, []string{openTracker})},
		{"lists and defaults", privateTorrent(t, []string{trA}, []string{openTracker})},
	}
	pushes := []struct {
		name string
		req  *pb.PushRequest
	}{
		{"uid upload", &pb.PushRequest{Torrent: privateTorrent(t, []string{trB}), Caller: &pb.Caller{Uid: uidB}, Source: "site"}},
		{"uid magnet", &pb.PushRequest{Torrent: privateTorrent(t, []string{trB, openTracker}), Caller: &pb.Caller{Uid: uidB}, Source: "magnet"}},
		{"no Caller upload", &pb.PushRequest{Torrent: makeTorrent(t, trB, metainfo.AnnounceList{{trB}}, seedURL, true)}},
		{"no Caller magnet", &pb.PushRequest{Torrent: privateTorrent(t, []string{openTracker})}},
		{"no Caller trackerless", &pb.PushRequest{Torrent: privateTorrent(t)}},
	}
	for _, s := range stored {
		for _, push := range pushes {
			name := s.name + "/" + push.name
			tiers := []*fakeProvider{newFakeProvider("l1", true), newFakeProvider("l2", true), newFakeProvider("s3", true)}
			srv := NewServer(NewStore([]StoreProvider{tiers[0], tiers[1], tiers[2]}), nil, nil, []string{openTracker})
			srv.SetPrivate(newRecords(newMemObjects()))
			h := infoHashOf(t, push.req.GetTorrent())
			for _, p := range tiers {
				if s.raw != nil {
					p.torrents[h] = s.raw
				}
			}
			if _, err := srv.Push(ctx, push.req); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, p := range tiers {
				got, ok := p.torrents[h]
				if !ok {
					t.Fatalf("%s: %s holds no copy", name, p.name)
				}
				if announce, webseeds := parseAnnounce(t, got); len(announce)+len(webseeds) != 0 || bytes.Contains(got, []byte("://")) || infoHashOf(t, got) != h {
					t.Fatalf("%s: %s stored %v %v", name, p.name, announce, webseeds)
				}
			}
		}
	}
}

func TestPushWithCallerPullDurableErrorWritesNothing(t *testing.T) {
	p := &failingPullProvider{newFakeProvider("mem", true)}
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)
	obj := newMemObjects()
	srv.SetPrivate(newRecords(obj))
	_, err := pushAs(srv, privateTorrent(t, []string{trA}), &pb.Caller{Uid: uidA}, nil)
	wantRefusal(t, err, codes.Unavailable, "")
	if len(p.torrents) != 0 || len(obj.objs) != 0 {
		t.Fatal("wrote after failing to read the stored copy")
	}
}

func TestPushDryRun(t *testing.T) {
	srv, p, obj := privateServer(t)
	a, a2 := privateTorrent(t, []string{trA}), privateTorrent(t, []string{trA2})
	outcome := func(raw []byte, mod func(*pb.PushRequest)) string {
		t.Helper()
		r, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, mod)
		if err != nil {
			t.Fatal(err)
		}
		return r.GetRecordOutcome()
	}
	if o := outcome(a, dry); o != "inserted" || len(p.torrents) != 0 || len(obj.objs) != 0 {
		t.Fatalf("dry on nothing = %s, wrote %d/%d", o, len(p.torrents), len(obj.objs))
	}
	outcome(a, nil)
	puts, copies := obj.count("put", ""), len(p.torrents)
	for _, c := range []struct {
		raw  []byte
		mod  func(*pb.PushRequest)
		want string
	}{
		{a, dry, "unchanged"},
		{a2, dry, "updated"},
	} {
		if o := outcome(c.raw, c.mod); o != c.want {
			t.Fatalf("dry = %s, want %s", o, c.want)
		}
	}
	_, err := pushAs(srv, privateTorrent(t), &pb.Caller{Uid: uidA}, dry)
	wantRefusal(t, err, codes.InvalidArgument, "private_no_tracker")
	pub := makeTorrent(t, trA, nil, nil, false)
	if r, err := pushAs(srv, pub, &pb.Caller{Uid: uidA}, dry); err != nil || r.GetRecordOutcome() != "" {
		t.Fatalf("dry public = %v, %v", r, err)
	}
	if obj.count("put", "") != puts || len(p.torrents) != copies {
		t.Fatal("dry_run wrote")
	}
}

// Old m2t returns [[tr..., DEFAULT_TRACKERS]], new m2t [[tr...]]: the same trackers, so the record is unchanged.
func TestPushMagnetPayloadShapesUnchanged(t *testing.T) {
	srv, _, _ := privateServer(t)
	magnet := func(r *pb.PushRequest) { r.Source = "magnet" }
	old, cur := privateTorrent(t, []string{trA, trB, openTracker}), privateTorrent(t, []string{trA, trB})
	for _, c := range []struct {
		raw  []byte
		mod  func(*pb.PushRequest)
		want string
	}{{old, magnet, "inserted"}, {cur, func(r *pb.PushRequest) { magnet(r); dry(r) }, "unchanged"}, {cur, magnet, "unchanged"}} {
		if r, err := pushAs(srv, c.raw, &pb.Caller{Uid: uidA}, c.mod); err != nil || r.GetRecordOutcome() != c.want {
			t.Fatalf("push = %v, %v; want %s", r, err, c.want)
		}
	}
}

func TestPushPublicWithCallerAsToday(t *testing.T) {
	srv, p, obj := privateServer(t)
	raw := makeTorrent(t, trA, nil, nil, false)
	r, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil)
	if err != nil || r.GetRecordOutcome() != "" || len(obj.ops) != 0 {
		t.Fatalf("public push = %v, %v, ops %q", r, err, obj.ops)
	}
	if announce, _ := parseAnnounce(t, p.torrents[infoHashOf(t, raw)]); !slices.Contains(announce, trA) {
		t.Fatal("public torrent lost its trackers")
	}
}

// A push without Caller is stripped like any other and writes no record.
func TestPushWithoutCaller(t *testing.T) {
	srv, p, obj := privateServer(t)
	raw := privateTorrent(t, []string{trA})
	before := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped))
	r, err := srv.Push(ctx, &pb.PushRequest{Torrent: raw})
	if err != nil || r.GetRecordOutcome() != "" || len(obj.ops) != 0 {
		t.Fatalf("%v, %v, ops %q", r, err, obj.ops)
	}
	if announce, _ := parseAnnounce(t, p.torrents[infoHashOf(t, raw)]); len(announce) != 0 {
		t.Fatalf("stored announce %v", announce)
	}
	if d := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped)) - before; d != 1 {
		t.Fatalf("stripped delta %v", d)
	}
}

// stripped stands for stored, merged and kept only: a copy that does not
// load is still overwritten blind, and that stays visible.
func TestPushStrippedLeavesBlind(t *testing.T) {
	srv, p, _ := privateServer(t)
	raw := privateTorrent(t, []string{trA})
	p.torrents[infoHashOf(t, raw)] = []byte("not a torrent")
	before := pushSeries()
	if r, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil); err != nil || r.GetRecordOutcome() != "inserted" {
		t.Fatalf("push = %v, %v", r, err)
	}
	after := pushSeries()
	for k := range after {
		if want := map[bool]float64{true: 1}[k == "true/"+pushBlind]; after[k]-before[k] != want {
			t.Fatalf("%s delta = %v, want %v", k, after[k]-before[k], want)
		}
	}
}

// Fields only; the scan for URLs, keys and uids over every private path is
// TestNoPlaintextInLogsOrErrors (task 7).
func TestPushRecordLogLine(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	srv, _, _ := privateServer(t)
	raw := privateTorrent(t, []string{trA})
	_, _ = pushAs(srv, raw, &pb.Caller{Uid: uidB}, func(r *pb.PushRequest) { r.Source = "rapidapi" })
	_, _ = pushAs(srv, raw, &pb.Caller{}, nil)
	_, _ = pushAs(srv, raw, &pb.Caller{Uid: uidB}, dry)
	var lines []*logrus.Entry
	for _, e := range hook.AllEntries() {
		if e.Data["event"] == "private_record_saved" {
			lines = append(lines, e)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("private_record_saved lines = %d, want 3", len(lines))
	}
	want := logrus.Fields{"event": "private_record_saved", "acct": "5f44da3a", "infohash": infoHashOf(t, raw), "source": "rapidapi", "outcome": "inserted", "hosts": "tracker.example", "urls": 1}
	if !reflect.DeepEqual(lines[0].Data, want) {
		t.Fatalf("line = %v, want %v", lines[0].Data, want)
	}
	if lines[1].Data["outcome"] != "refused" || lines[1].Data["reason"] != "private_needs_account" || lines[1].Data["hosts"] != "tracker.example" {
		t.Fatalf("refusal line = %v", lines[1].Data)
	}
	if lines[2].Data["dry_run"] != true || lines[2].Data["outcome"] != "unchanged" {
		t.Fatalf("dry_run line = %v", lines[2].Data)
	}
}

// recordLine is the one private_record_saved line since the last call; it resets hook.
func recordLine(t *testing.T, hook *logtest.Hook) *logrus.Entry {
	t.Helper()
	defer hook.Reset()
	var line *logrus.Entry
	for _, e := range hook.AllEntries() {
		if e.Data["event"] == "private_record_saved" {
			if line != nil {
				t.Fatal("two private_record_saved lines")
			}
			line = e
		}
	}
	if line == nil {
		t.Fatal("no private_record_saved line")
	}
	return line
}

// raw is untrusted before Normalize: hosts stays bounded however long or many
// the URLs, and a source the push was refused for is not echoed.
func TestPushRecordLogLineBounded(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	srv, _, _ := privateServer(t)

	long := "http://" + strings.Repeat("a", 3000) + ".example/announce"
	_, _ = pushAs(srv, privateTorrent(t, []string{long}), &pb.Caller{Uid: uidA}, nil)
	l := recordLine(t, hook)
	if h := l.Data["hosts"].(string); l.Data["reason"] != "private_no_tracker" || len(h) > maxLogHosts+len("…") || !strings.HasSuffix(h, "…") {
		t.Fatalf("long host: reason %v, hosts %d bytes", l.Data["reason"], len(h))
	}

	many := make([][]string, 100)
	for i := range many {
		many[i] = []string{fmt.Sprintf("http://t%d.example/announce", i)}
	}
	_, _ = pushAs(srv, privateTorrent(t, many...), &pb.Caller{Uid: uidA}, nil)
	l = recordLine(t, hook)
	if h := strings.Split(l.Data["hosts"].(string), ","); l.Data["reason"] != "private_too_many_trackers" || len(h) != maxLogURLs || h[0] != "t0.example" || l.Data["urls"] != 100 {
		t.Fatalf("100 hosts: %v", l.Data)
	}

	_, _ = pushAs(srv, privateTorrent(t, []string{trA}), &pb.Caller{Uid: uidA}, func(r *pb.PushRequest) { r.Source = "http://tracker.example/" })
	if l = recordLine(t, hook); l.Data["source"] != "invalid" || l.Data["reason"] != "invalidargument" {
		t.Fatalf("bad source: %v", l.Data)
	}
}

// A record upsert its caller abandoned, on the dry_run and the write path,
// answers with ctx's status, logs failed with it as the reason, and a write
// counts as an error.
func TestPushRecordCallerGone(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, cancel := context.WithDeadline(ctx, time.Now())
	defer cancel()
	raw := privateTorrent(t, []string{trA})
	for _, c := range []struct {
		ctx  context.Context
		code codes.Code
	}{{canceled, codes.Canceled}, {expired, codes.DeadlineExceeded}} {
		for _, isDry := range []bool{true, false} {
			srv, _, obj := privateServer(t)
			obj.failOn("get", "private/", awserr.New(request.CanceledErrorCode, "request context canceled", c.ctx.Err()))
			errs := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushError))
			_, err := srv.Push(c.ctx, &pb.PushRequest{Torrent: raw, Caller: &pb.Caller{Uid: uidA}, Source: "site", DryRun: isDry})
			wantRefusal(t, err, c.code, "")
			if l := recordLine(t, hook); l.Data["outcome"] != "failed" || l.Data["reason"] != strings.ToLower(c.code.String()) || (l.Data["dry_run"] == true) != isDry {
				t.Fatalf("%v dry_run=%v: line %v", c.code, isDry, l.Data)
			}
			want := map[bool]float64{false: 1}[isDry] // dry_run is never counted
			if d := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushError)) - errs; d != want {
				t.Fatalf("%v dry_run=%v: push error delta = %v, want %v", c.code, isDry, d, want)
			}
		}
	}
}

// The record fails after the copy was written: Unavailable, a Warn line with
// outcome failed, and the info-only copy stays; a retry writes the record.
func TestPushRecordFailsAfterCopy(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	srv, p, obj := privateServer(t)
	obj.failOn("get", "private/", errors.New("down"))
	raw := privateTorrent(t, []string{trA})
	if _, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil); status.Code(err) != codes.Unavailable {
		t.Fatalf("push err = %v, want Unavailable", err)
	}
	if l := recordLine(t, hook); l.Level != logrus.WarnLevel || l.Data["outcome"] != "failed" || l.Data["reason"] != "unavailable" {
		t.Fatalf("line %v: %v", l.Level, l.Data)
	}
	if announce, _ := parseAnnounce(t, p.torrents[infoHashOf(t, raw)]); len(announce) != 0 {
		t.Fatalf("copy announce = %v, want info-only", announce)
	}
}

func TestPrivatePushCounted(t *testing.T) {
	srv, _, _ := privateServer(t)
	before := pushSeries()
	_, _ = pushAs(srv, privateTorrent(t), &pb.Caller{Uid: uidA}, nil)                // private_no_tracker
	_, _ = pushAs(srv, privateTorrent(t, []string{trA}), &pb.Caller{Uid: uidA}, dry) // not counted
	after := pushSeries()
	for k := range after {
		want := map[bool]float64{true: 1}[k == "true/"+pushRefused]
		if after[k]-before[k] != want {
			t.Fatalf("%s delta = %v, want %v", k, after[k]-before[k], want)
		}
	}
}
