package services

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
)

// Every private path once, with a logrus hook: no URL, passkey, uid or
// tiers_enc in any log line or status text (§6 «Логи», §10). This is the one
// scan of the repo; other tests assert fields only.
func TestNoPlaintextInLogsOrErrors(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	srv, _, obj := privateServer(t, true)
	raw := privateTorrent(t, []string{trA})
	h := infoHashOf(t, raw)
	var errs []error
	note := func(_ any, err error) { errs = append(errs, err) }
	note(pushAs(srv, raw, &pb.Caller{Uid: uidA}, nil))
	note(pushAs(srv, raw, &pb.Caller{}, nil))
	note(pushAs(srv, privateTorrent(t, urlTiers(33)...), &pb.Caller{Uid: uidA}, nil))
	note(srv.GetPrivateRecord(ctx, &pb.GetPrivateRecordRequest{InfoHash: h, Uid: uidA}))
	note(srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidB}}))
	note(srv.Files(ctx, &pb.FilesRequest{InfoHash: h}))
	note(srv.Touch(ctx, &pb.TouchRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidA}}))
	note(srv.ListPrivateRecords(ctx, &pb.ListPrivateRecordsRequest{Uid: uidA}))
	other, _ := privaterecord.Seal(privaterecord.Vector.Key, strings.Repeat("ab", 20), uidB, [][]string{{trB}})
	seed(t, obj, h, uidB, other)
	note(srv.GetPrivateRecord(ctx, &pb.GetPrivateRecordRequest{InfoHash: h, Uid: uidB}))
	note(srv.Pull(ctx, &pb.PullRequest{InfoHash: h, Caller: &pb.Caller{Uid: uidB}})) // unreadable
	note(srv.DeletePrivateRecordsByUser(ctx, &pb.DeletePrivateRecordsByUserRequest{Uid: uidA}))
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
}

// Only the series an alert or a gate reads exist before the first event:
// G2 reads caller="legacy", PrivateRecordsBad and G2 read outcome="unreadable".
func TestPrivateSeriesStartAtZero(t *testing.T) {
	families, _ := prometheus.DefaultGatherer.Gather()
	have := map[string]bool{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			key := f.GetName()
			for _, l := range m.GetLabel() { // sorted by label name: caller, outcome
				key += " " + l.GetValue()
			}
			have[key] = true
		}
	}
	for _, series := range []string{
		"torrent_store_private_access_total legacy allowed",
		"torrent_store_private_access_total legacy sign_in",
		"torrent_store_private_access_total uid unreadable",
		"torrent_store_private_enforce",
	} {
		if !have[series] {
			t.Errorf("series %q does not exist at start", series)
		}
	}
}
