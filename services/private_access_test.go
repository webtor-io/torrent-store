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
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func accessCount(caller, outcome string) float64 {
	return testutil.ToFloat64(privateAccessTotal.WithLabelValues(caller, outcome))
}

// seedPrivate stores a copy that still carries trA (stored by keep-first before phase 1)
// and a record for uidA with [[trB]].
func seedPrivate(t *testing.T, srv *Server, p *fakeProvider, private bool) string {
	t.Helper()
	raw := makeTorrent(t, "", metainfo.AnnounceList{{trA}}, nil, private)
	h := infoHashOf(t, raw)
	if _, err := p.Push(ctx, h, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.p.upsert(ctx, h, uidA, [][]string{{trB}}, "site", true); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPrivateAccessMatrix(t *testing.T) {
	callers := []struct {
		name string
		c    *pb.Caller
	}{{"legacy", nil}, {"internal", &pb.Caller{Internal: true}}, {"internal uid", &pb.Caller{Uid: uidA, Internal: true}}, {"anon", &pb.Caller{}}, {"no record", &pb.Caller{Uid: uidB}}, {"record", &pb.Caller{Uid: uidA}}}
	want := func(private bool, caller string) (codes.Code, string) {
		switch {
		case !private, strings.HasPrefix(caller, "internal"), caller == "record":
			return codes.OK, ""
		case caller == "no record":
			return codes.PermissionDenied, "no_record"
		}
		return codes.PermissionDenied, "sign_in" // anon, legacy
	}
	for _, private := range []bool{false, true} {
		srv, p, obj := privateServer(t)
		h := seedPrivate(t, srv, p, private)
		mark := len(obj.ops)
		for _, c := range callers {
			name := fmt.Sprintf("private=%v %s", private, c.name)
			code, reason := want(private, c.name)
			pull, err := srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: c.c})
			wantRefusal(t, err, code, reason)
			// A uid's record brings its own trackers; everyone else gets the
			// stored copy, a private one without trackers.
			if mine := private && c.name == "record"; err == nil {
				switch announce, _ := parseAnnounce(t, pull.GetTorrent()); {
				case pull.GetCallerTrackers() != mine:
					t.Fatalf("%s: caller_trackers = %v", name, pull.GetCallerTrackers())
				case mine && !slices.Equal(announce, []string{trB}):
					t.Fatalf("%s: Pull announce = %v, want the record's", name, announce)
				case !mine && !private && !bytes.Equal(pull.GetTorrent(), p.torrents[h]):
					t.Fatalf("%s: Pull is not the stored copy as is", name)
				case !mine && (len(announce) > 0) == private:
					t.Fatalf("%s: Pull announce = %v", name, announce)
				}
			}
			_, err = srv.Files(ctx, &pb.FilesRequest{InfoHash: h, Caller: c.c})
			wantRefusal(t, err, code, reason)
			touch, err := srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: c.c})
			wantRefusal(t, err, code, reason)
			if err == nil && touch.GetAccess() != map[bool]string{true: "allowed"}[c.c != nil] {
				t.Fatalf("%s: Touch access = %q", name, touch.GetAccess())
			}
		}
		if !private && len(obj.ops) != mark {
			t.Fatalf("public torrent reached the bucket: %q", obj.ops[mark:])
		}
	}
}

// A uid's Pull answers with its record's tiers and nothing of the stored copy,
// keep-first trackers and webseeds included, from one GET.
func TestPullCallerTrackers(t *testing.T) {
	const trB2 = "udp://tracker.invalid:6969/b2/00000000000000000000000000000000/announce"
	tiersA := [][]string{{trB, trB2}, {trA2}}
	tiersB := [][]string{{trB2}}
	srv, p, obj := privateServer(t)
	stored := makeTorrent(t, trA, metainfo.AnnounceList{{trA}, {openTracker}}, metainfo.UrlList{"http://seed.example/00000000000000000000000000000000/"}, true)
	h := infoHashOf(t, stored)
	p.torrents[h] = stored // keep-first's copy from before records
	empty, err := privaterecord.Seal(privaterecord.Vector.Key, h, uidC, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, obj, h, uidC, empty) // authentic, but no tier: still nothing of the copy
	for _, c := range []struct {
		uid   string
		tiers [][]string
	}{{uidA, tiersA}, {uidB, tiersB}, {uidC, nil}} {
		if c.tiers != nil {
			if _, err := srv.p.upsert(ctx, h, c.uid, c.tiers, "site", true); err != nil {
				t.Fatal(err)
			}
		}
		name := privaterecord.Acct(c.uid)
		gets := obj.count("get", "private/")
		r, err := srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: c.uid}})
		if err != nil || !r.GetCallerTrackers() {
			t.Fatalf("%s: %v, caller_trackers %v", name, err, r.GetCallerTrackers())
		}
		if n := obj.count("get", "private/") - gets; n != 1 {
			t.Fatalf("%s: %d record GETs, want 1", name, n)
		}
		mi, err := metainfo.Load(bytes.NewReader(r.GetTorrent()))
		if err != nil || mi.HashInfoBytes().HexString() != h {
			t.Fatalf("%s: load = %v, %v; want info hash %s", name, mi, err, h)
		}
		announce := ""
		if len(c.tiers) > 0 {
			announce = c.tiers[0][0]
		}
		if mi.Announce != announce || !reflect.DeepEqual([][]string(mi.AnnounceList), c.tiers) || len(mi.UrlList) != 0 {
			t.Fatalf("%s: announce %q, announce-list %q, url-list %q; want %q, %q, none", name, mi.Announce, mi.AnnounceList, mi.UrlList, announce, c.tiers)
		}
		for _, leak := range []string{trA, openTracker, "seed.example"} {
			if bytes.Contains(r.GetTorrent(), []byte(leak)) {
				t.Fatalf("%s: reply carries the stored copy's %q", name, leak)
			}
		}
	}
}

func TestPrivateAccessFailsClosed(t *testing.T) {
	srv, p, obj := privateServer(t)
	h := seedPrivate(t, srv, p, true)
	pull := func(c *pb.Caller) error {
		_, err := srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: c})
		return err
	}
	before := accessCount("uid", "unreadable")
	other, err := privaterecord.Seal(privaterecord.Vector.Key, strings.Repeat("ab", 20), uidB, [][]string{{trB}})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, obj, h, uidB, other)
	wantRefusal(t, pull(&pb.Caller{Uid: uidB}), codes.PermissionDenied, "no_record")
	if accessCount("uid", "unreadable")-before != 1 {
		t.Fatal("unreadable record not counted as unreadable")
	}
	wantRefusal(t, pull(&pb.Caller{Uid: "../" + uidA[3:]}), codes.InvalidArgument, "")
	obj.failOn("get", "private/", errors.New("down"))
	wantRefusal(t, pull(&pb.Caller{Uid: uidA}), codes.Unavailable, "") // never no_record
	srv.SetPrivate(NewPrivateRecords(obj, nil))                        // no key
	wantRefusal(t, pull(&pb.Caller{Uid: uidA}), codes.FailedPrecondition, "private_unavailable")
	srv.SetPrivate(NewPrivateRecords(nil, [][]byte{privaterecord.Vector.Key})) // no S3
	wantRefusal(t, pull(&pb.Caller{Uid: uidA}), codes.FailedPrecondition, "private_unavailable")
	wantRefusal(t, pull(&pb.Caller{Internal: true}), codes.OK, "")
}

// A record GET whose caller went away failed on the caller, not on S3: the SDK
// answers it RequestCanceled wrapping ctx's error, as aws/corehandlers does.
// A real S3 error under a live context is still unavailable.
func TestAccessCallerGone(t *testing.T) {
	hook := logtest.NewGlobal()
	srv, p, obj := privateServer(t)
	h := seedPrivate(t, srv, p, true)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, cancel := context.WithDeadline(ctx, time.Now())
	defer cancel()
	sdkCanceled := func(ctx context.Context) error {
		return awserr.New(request.CanceledErrorCode, "request context canceled", ctx.Err())
	}
	for _, c := range []struct {
		name    string
		ctx     context.Context
		err     error
		code    codes.Code
		outcome string
	}{
		{"canceled", canceled, sdkCanceled(canceled), codes.Canceled, "canceled"},
		{"deadline", expired, sdkCanceled(expired), codes.DeadlineExceeded, "timeout"},
		{"body read canceled", canceled, context.Canceled, codes.Canceled, "canceled"},
		{"S3 error", ctx, awserr.New("SlowDown", "reduce your request rate", nil), codes.Unavailable, "unavailable"},
	} {
		obj.failOn("get", "private/", c.err)
		hook.Reset()
		before := accessCount("uid", c.outcome)
		_, err := srv.access(c.ctx, h, &pb.Caller{Uid: uidA})
		wantRefusal(t, err, c.code, "")
		if accessCount("uid", c.outcome)-before != 1 {
			t.Fatalf("%s: uid/%s not counted", c.name, c.outcome)
		}
		warned := slices.ContainsFunc(hook.AllEntries(), func(e *log.Entry) bool {
			return e.Message == "private records: S3 request failed"
		})
		if warned != (c.code == codes.Unavailable) {
			t.Fatalf("%s: S3 failure logged = %v", c.name, warned)
		}
	}
	// The record RPCs share the answer.
	obj.failOn("list", "private/", sdkCanceled(canceled))
	if _, err := srv.ListPrivateRecords(canceled, &pb.ListPrivateRecordsRequest{Uid: uidA}); status.Code(err) != codes.Canceled {
		t.Fatalf("list = %v, want Canceled", err)
	}
	if _, err := srv.DeletePrivateRecordsByUser(expired, &pb.DeletePrivateRecordsByUserRequest{Uid: uidA}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("delete by user = %v, want DeadlineExceeded", err)
	}
}

func TestFilesChecksAccessOnCachedManifest(t *testing.T) {
	srv, p, _ := privateServer(t)
	h := seedPrivate(t, srv, p, true)
	for _, c := range []struct {
		caller *pb.Caller
		code   codes.Code
		reason string
	}{{&pb.Caller{Internal: true}, codes.OK, ""}, {&pb.Caller{Uid: uidB}, codes.PermissionDenied, "no_record"}, {&pb.Caller{Uid: uidA}, codes.OK, ""}} {
		_, err := srv.Files(ctx, &pb.FilesRequest{InfoHash: h, Caller: c.caller})
		wantRefusal(t, err, c.code, c.reason)
	}
}

func TestPrivateAccessCounted(t *testing.T) {
	srv, p, _ := privateServer(t)
	h := seedPrivate(t, srv, p, true)
	for _, c := range []struct {
		caller         *pb.Caller
		class, outcome string
	}{{nil, "legacy", "sign_in"}, {&pb.Caller{Internal: true}, "internal", "allowed"}, {&pb.Caller{}, "uid", "sign_in"}, {&pb.Caller{Uid: uidB}, "uid", "no_record"}, {&pb.Caller{Uid: uidA}, "uid", "allowed"}} {
		before := accessCount(c.class, c.outcome)
		_, _ = srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: c.caller})
		if accessCount(c.class, c.outcome)-before != 1 {
			t.Fatalf("%s/%s not counted", c.class, c.outcome)
		}
	}
}

// Touch with an internal Caller reads no manifest: stored bytes the manifest
// build cannot parse still answer OK. Every other Touch reads it, one without
// Caller included, so the same bytes fail.
func TestTouchManifestSkipped(t *testing.T) {
	srv, p, _ := privateServer(t)
	h := strings.Repeat("cd", 20)
	if _, err := p.Push(ctx, h, []byte("torrent")); err != nil {
		t.Fatal(err)
	}
	if touch, err := srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: &pb.Caller{Internal: true}}); err != nil || touch.GetAccess() != "allowed" {
		t.Fatalf("internal Touch = %q, %v", touch.GetAccess(), err)
	}
	for _, c := range []*pb.Caller{nil, {Uid: uidA}} {
		if _, err := srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: c}); err == nil {
			t.Fatalf("Touch(%v) answered without reading the manifest", c)
		}
	}
}
