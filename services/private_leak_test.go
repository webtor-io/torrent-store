package services

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every private path once, with a logrus hook: no URL, passkey, uid or
// tiers_enc in any log line or status text (§6 «Логи», §10). This is the one
// scan of the repo; other tests assert fields only.
func TestNoPlaintextInLogsOrErrors(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	srv, _, obj := privateServer(t, true)
	off := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, []string{openTracker})
	off.SetPrivate(NewPrivateRecords(nil, [][]byte{privaterecord.Vector.Key}), true)
	raw := privateTorrent(t, []string{trA})
	h := infoHashOf(t, raw)
	var errs []error
	note := func(_ any, err error) { errs = append(errs, err) }
	note(pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil))                                // inserted
	note(pushAs(srv, privateTorrent(t, []string{trA2}), &pb.Caller{Uid: uidA}, nil))  // updated
	note(pushAs(srv, raw, &pb.Caller{Uid: uidA}, dry))                                // dry_run
	note(srv.Push(ctx, &pb.PushRequest{Torrent: raw}))                                // stripped
	note(pushAs(srv, raw, &pb.Caller{}, nil))                                         // needs_account
	note(pushAs(srv, privateTorrent(t, urlTiers(33)...), &pb.Caller{Uid: uidA}, nil)) // too_many
	note(pushAs(off, raw, &pb.Caller{Uid: uidA}, nil))                                // private_unavailable
	found, err := srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidA}})
	note(found, err)                                                                 // the record's trackers
	note(srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidB}})) // no_record
	note(srv.Files(ctx, &pb.FilesRequest{InfoHash: h}))                              // sign_in
	note(srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidA}}))
	note(srv.ListPrivateRecords(ctx, &pb.ListPrivateRecordsRequest{Uid: uidA}))
	other, err := privaterecord.Seal(privaterecord.Vector.Key, strings.Repeat("ab", 20), uidB, [][]string{{trB}})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, obj, h, uidB, other)
	note(srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidB}})) // unreadable
	// An S3 request error names the object URL, and with it the uid.
	obj.failOn("get", "private/", fmt.Errorf("RequestError: send request failed caused by: Get \"https://bucket.invalid/%s\": i/o timeout", recordKey(uidA, h)))
	note(srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidA}}))
	note(pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil))
	obj.failOn("get", "private/", nil)
	note(srv.DeletePrivateRecordsByUser(ctx, &pb.DeletePrivateRecordsByUserRequest{Uid: uidA}))

	// Each path ran as listed, so the scan below has something to scan.
	OK, PD, IA, FP, UN := codes.OK, codes.PermissionDenied, codes.InvalidArgument, codes.FailedPrecondition, codes.Unavailable
	want := []codes.Code{OK, OK, OK, OK, PD, IA, FP, OK, PD, PD, OK, OK, PD, UN, UN, OK}
	if len(errs) != len(want) {
		t.Fatalf("%d calls, want %d", len(errs), len(want))
	}
	for i, err := range errs {
		if status.Code(err) != want[i] {
			t.Fatalf("call %d: %v, want %v", i, err, want[i])
		}
	}
	if !found.GetCallerTrackers() {
		t.Fatal("Pull with the record's uid came without its trackers")
	}
	saved := false
	for _, e := range hook.AllEntries() {
		saved = saved || e.Data["event"] == "private_record_saved"
	}
	if !saved {
		t.Fatal("no private_record_saved line reached the hook")
	}

	enc := base64.StdEncoding.EncodeToString(other)
	forbidden := []string{"://", "00000000000000000000000000000000", uidA, uidB, enc[:16]}
	var texts []string
	for _, e := range hook.AllEntries() {
		texts = append(texts, e.Message)
		for _, v := range e.Data {
			texts = append(texts, fmt.Sprint(v))
		}
	}
	for _, err := range errs {
		if err != nil {
			texts = append(texts, err.Error())
		}
	}
	for _, s := range texts {
		for _, f := range forbidden {
			if strings.Contains(s, f) {
				t.Fatalf("%q leaks %q", s, f)
			}
		}
	}
	// Hosts are logged by private_record_saved alone, never by Pull.
	for _, e := range hook.AllEntries() {
		if e.Data["event"] == "private_record_saved" {
			continue
		}
		line, _ := e.String()
		for _, host := range []string{"tracker.example", "tracker.invalid"} {
			if strings.Contains(line, host) {
				t.Fatalf("%q names tracker host %q", line, host)
			}
		}
	}
}
