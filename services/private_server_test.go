package services

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// wantRefusal: code OK means err == nil; reason "" means the status carries no ErrorInfo.
func wantRefusal(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	if status.Code(err) != code || refusalReason(err) != reason {
		t.Fatalf("err = %v, reason %q; want %v, reason %q", err, refusalReason(err), code, reason)
	}
}

// recordsServer is a Server whose private records live in obj.
func recordsServer(obj *memObjects) *Server {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	srv.SetPrivate(newRecords(obj), false)
	return srv
}

// seedRecord upserts tiers for (h, uid) with source "site".
func seedRecord(t *testing.T, obj *memObjects, h, uid string, tiers [][]string) {
	t.Helper()
	if _, err := newRecords(obj).upsert(ctx, h, uid, tiers, "site", true); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRPCsRejectMalformedIDs(t *testing.T) {
	srv, p, obj := privateServer(t, false)
	h := seedPrivate(t, srv, p, true)
	mark := len(obj.ops)
	for _, bad := range []string{"", strings.ToUpper(uidA), "../" + uidA[3:], uidA + "/", uidA[:39]} {
		calls := map[string]error{}
		_, calls["list"] = srv.ListPrivateRecords(ctx, &pb.ListPrivateRecordsRequest{Uid: bad})
		_, calls["by user"] = srv.DeletePrivateRecordsByUser(ctx, &pb.DeletePrivateRecordsByUserRequest{Uid: bad})
		if bad != "" { // a Caller with no uid is sign_in
			_, calls["pull uid"] = srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: bad}})
		}
		if bad != strings.ToUpper(uidA) { // an upper-case hash is lower-cased, an upper-case uid is not
			p.torrents[bad] = p.torrents[h] // found by the store: only access checks the hash
			_, calls["pull hash"] = srv.Pull(ctx, &pb.PullRequest{InfoHash: bad, Caller: &pb.Caller{Uid: uidA}})
		}
		for name, err := range calls {
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("%s(%q) = %v, want InvalidArgument", name, bad, err)
			}
		}
	}
	if len(obj.ops) != mark {
		t.Fatalf("malformed ids reached the bucket: %q", obj.ops[mark:])
	}
}

func TestListPrivateRecords(t *testing.T) {
	obj := newMemObjects()
	h2, h3 := strings.Repeat("cd", 20), strings.Repeat("ef", 20)
	seedRecord(t, obj, testHash, uidA, [][]string{{trA}})
	seedRecord(t, obj, h2, uidA, [][]string{{trB}})
	seedRecord(t, obj, testHash, uidB, [][]string{{trB}}) // another account's: not listed
	obj.objs[recordKey(uidA, h3)] = []byte("{")           // undecodable JSON: listed by its hash alone
	r, err := recordsServer(obj).ListPrivateRecords(ctx, &pb.ListPrivateRecordsRequest{Uid: uidA})
	if err != nil || len(r.GetRecords()) != 3 {
		t.Fatalf("list = %v, %v", r, err)
	}
	got := r.GetRecords()[0]
	if got.GetInfoHash() != testHash || !reflect.DeepEqual(got.GetHosts(), []string{"tracker.example"}) || got.GetSource() != "site" || got.GetCreatedAt() == 0 || got.GetUpdatedAt() == 0 {
		t.Fatalf("first = %v", got)
	}
	if last := r.GetRecords()[2]; last.GetInfoHash() != h3 || len(last.GetHosts()) != 0 {
		t.Fatalf("undecodable record = %v, want its hash alone", last)
	}
	obj.failOn("get", recordKey(uidA, h2), errors.New("down"))
	if _, err := recordsServer(obj).ListPrivateRecords(ctx, &pb.ListPrivateRecordsRequest{Uid: uidA}); status.Code(err) != codes.Unavailable {
		t.Fatalf("one record unreadable from S3: %v, want Unavailable, not a partial list", err)
	}
}

func TestDeletePrivateRecordsByUser(t *testing.T) {
	obj := newMemObjects()
	h2 := strings.Repeat("cd", 20)
	seedRecord(t, obj, testHash, uidA, [][]string{{trA}})
	seedRecord(t, obj, h2, uidA, [][]string{{trB}})
	seedRecord(t, obj, testHash, uidB, [][]string{{trB}})
	srv := recordsServer(obj)
	if r, err := srv.DeletePrivateRecordsByUser(ctx, &pb.DeletePrivateRecordsByUserRequest{Uid: uidA}); err != nil || r.GetDeleted() != 2 {
		t.Fatalf("by user = %v, %v", r, err)
	}
	if _, ok := obj.objs[recordKey(uidB, testHash)]; !ok || len(obj.objs) != 1 {
		t.Fatalf("left: %v; want only the other account's record", slices.Collect(maps.Keys(obj.objs)))
	}
}
