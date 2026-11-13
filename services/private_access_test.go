package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
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
	}{{"legacy", nil}, {"internal", &pb.Caller{Internal: true}}, {"anon", &pb.Caller{}}, {"no record", &pb.Caller{Uid: uidB}}, {"record", &pb.Caller{Uid: uidA}}}
	want := func(private, enforce bool, caller string) (codes.Code, string) {
		switch {
		case !private, caller == "internal", caller == "record", caller == "legacy" && !enforce:
			return codes.OK, ""
		case caller == "no record":
			return codes.PermissionDenied, "no_record"
		}
		return codes.PermissionDenied, "sign_in" // anon; legacy under enforce
	}
	for _, enforce := range []bool{false, true} {
		for _, private := range []bool{false, true} {
			srv, p, obj := privateServer(t, enforce)
			h := seedPrivate(t, srv, p, private)
			mark := len(obj.ops)
			for _, c := range callers {
				name := fmt.Sprintf("enforce=%v private=%v %s", enforce, private, c.name)
				code, reason := want(private, enforce, c.name)
				pull, err := srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: c.c})
				wantRefusal(t, err, code, reason)
				if err == nil {
					if announce, _ := parseAnnounce(t, pull.GetTorrent()); (len(announce) > 0) != !(private && enforce) {
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
}

func TestPrivateAccessFailsClosed(t *testing.T) {
	srv, p, obj := privateServer(t, false)
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
	srv.SetPrivate(NewPrivateRecords(nil, [][]byte{privaterecord.Vector.Key}), false)
	wantRefusal(t, pull(&pb.Caller{Uid: uidA}), codes.FailedPrecondition, "private_unavailable")
	wantRefusal(t, pull(&pb.Caller{Internal: true}), codes.OK, "")
}

func TestFilesChecksAccessOnCachedManifest(t *testing.T) {
	srv, p, _ := privateServer(t, false)
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
	srv, p, _ := privateServer(t, false)
	h := seedPrivate(t, srv, p, true)
	for _, c := range []struct {
		caller         *pb.Caller
		enforce        bool
		class, outcome string
	}{{nil, false, "legacy", "allowed"}, {nil, true, "legacy", "sign_in"}, {&pb.Caller{Internal: true}, false, "internal", "allowed"}, {&pb.Caller{}, false, "uid", "sign_in"}, {&pb.Caller{Uid: uidB}, false, "uid", "no_record"}, {&pb.Caller{Uid: uidA}, false, "uid", "allowed"}} {
		srv.SetPrivate(srv.p, c.enforce)
		before := accessCount(c.class, c.outcome)
		_, _ = srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: c.caller})
		if accessCount(c.class, c.outcome)-before != 1 {
			t.Fatalf("%s/%s not counted", c.class, c.outcome)
		}
	}
}

// Touch with an internal Caller, or without one in observation, reads no
// manifest: stored bytes the manifest build cannot parse still answer OK. A
// uid Caller reads it, so the same bytes fail.
func TestTouchManifestSkipped(t *testing.T) {
	srv, p, _ := privateServer(t, false)
	h := strings.Repeat("cd", 20)
	if _, err := p.Push(ctx, h, []byte("torrent")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		caller *pb.Caller
		access string
	}{{&pb.Caller{Internal: true}, "allowed"}, {nil, ""}} {
		touch, err := srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: c.caller})
		if err != nil || touch.GetAccess() != c.access {
			t.Fatalf("Touch(%v) = %q, %v; want %q", c.caller, touch.GetAccess(), err, c.access)
		}
	}
	if _, err := srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidA}}); err == nil {
		t.Fatal("Touch with a uid answered without reading the manifest")
	}
}
