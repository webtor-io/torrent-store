package services

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
)

// privateServer: one fake tier, DEFAULT_TRACKERS = [openTracker], records in obj.
func privateServer(t *testing.T, enforce bool) (*Server, *fakeProvider, *memObjects) {
	t.Helper()
	p, obj := newFakeProvider("mem", true), newMemObjects()
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, []string{openTracker})
	srv.SetPrivate(newRecords(obj), enforce)
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
			srv.SetPrivate(records, false)
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
	srv.SetPrivate(newRecords(obj), false)
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
	got, _ := srv.GetPrivateRecord(ctx, &pb.GetPrivateRecordRequest{InfoHash: h, Uid: uidA})
	if len(got.GetTiers()) != 2 || got.GetTiers()[0].GetUrls()[0] != trA || got.GetTiers()[1].GetUrls()[0] != trA2 {
		t.Fatalf("record tiers = %v, want [[trA] [trA2]] without the default tracker", got.GetTiers())
	}
	if rec := obj.record(t, h, uidA); bytes.Contains(rec.TiersEnc, []byte("tracker.example")) || rec.Source != "site" {
		t.Fatalf("record = %+v", rec)
	}
	if d := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped)) - before; d != 1 {
		t.Fatalf("stripped delta = %v", d)
	}
}

func TestPushSecondUploaderGetsOwnRecord(t *testing.T) {
	srv, _, obj := privateServer(t, false)
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
	if got, _ := srv.GetPrivateRecord(ctx, &pb.GetPrivateRecordRequest{InfoHash: h, Uid: uidB}); !got.GetFound() || got.GetTiers()[0].GetUrls()[0] != trB {
		t.Fatalf("second uploader's record = %v", got)
	}
	if obj.count("put", "private/") != 2 {
		t.Fatalf("record puts = %d, want 2 (the repeat is unchanged)", obj.count("put", "private/"))
	}
}

func TestPushWithCallerLeavesFirstUploaderCopy(t *testing.T) {
	srv, p, _ := privateServer(t, false)
	raw := privateTorrent(t, []string{trB})
	h := infoHashOf(t, raw)
	p.torrents[h] = makeTorrent(t, "", metainfo.AnnounceList{{trA}, {openTracker}}, nil, true) // stored by keep-first before phase 1
	if r, err := pushAs(srv, raw, &pb.Caller{Uid: uidB}, nil); err != nil || r.GetRecordOutcome() != "inserted" {
		t.Fatalf("push = %v, %v", r, err)
	}
	if announce, _ := parseAnnounce(t, p.torrents[h]); !reflect.DeepEqual(announce, []string{trA}) {
		t.Fatalf("copy announce = %v, want the first uploader's only, defaults cleaned", announce)
	}
}

func TestPushWithCallerPullDurableErrorWritesNothing(t *testing.T) {
	p := &failingPullProvider{newFakeProvider("mem", true)}
	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)
	obj := newMemObjects()
	srv.SetPrivate(newRecords(obj), false)
	_, err := pushAs(srv, privateTorrent(t, []string{trA}), &pb.Caller{Uid: uidA}, nil)
	wantRefusal(t, err, codes.Unavailable, "")
	if len(p.torrents) != 0 || len(obj.objs) != 0 {
		t.Fatal("wrote after failing to read the stored copy")
	}
}

func TestPushDryRun(t *testing.T) {
	srv, p, obj := privateServer(t, false)
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
	srv, _, _ := privateServer(t, false)
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
	srv, p, obj := privateServer(t, true)
	raw := makeTorrent(t, trA, nil, nil, false)
	r, err := pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil)
	if err != nil || r.GetRecordOutcome() != "" || len(obj.ops) != 0 {
		t.Fatalf("public push = %v, %v, ops %q", r, err, obj.ops)
	}
	if announce, _ := parseAnnounce(t, p.torrents[infoHashOf(t, raw)]); !slices.Contains(announce, trA) {
		t.Fatal("public torrent lost its trackers")
	}
}

func TestPushWithoutCaller(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		srv, p, obj := privateServer(t, enforce)
		raw := privateTorrent(t, []string{trA})
		before := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped))
		r, err := srv.Push(ctx, &pb.PushRequest{Torrent: raw})
		if err != nil || r.GetRecordOutcome() != "" || len(obj.ops) != 0 {
			t.Fatalf("enforce=%v: %v, %v, ops %q", enforce, r, err, obj.ops)
		}
		announce, _ := parseAnnounce(t, p.torrents[infoHashOf(t, raw)])
		if kept := len(announce) > 0; kept == enforce {
			t.Fatalf("enforce=%v: stored announce %v", enforce, announce)
		}
		if d := testutil.ToFloat64(pushTotal.WithLabelValues("true", pushStripped)) - before; d != map[bool]float64{false: 0, true: 1}[enforce] {
			t.Fatalf("enforce=%v: stripped delta %v", enforce, d)
		}
	}
}

// stripped stands for stored, merged, kept and held only: a copy that does not
// load is still overwritten blind, and that stays visible.
func TestPushStrippedLeavesBlind(t *testing.T) {
	srv, p, _ := privateServer(t, false)
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
	srv, _, _ := privateServer(t, false)
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

func TestPrivatePushCounted(t *testing.T) {
	srv, _, _ := privateServer(t, false)
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
