package services

import (
	"bytes"
	"slices"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func makeTorrent(t *testing.T, announce string, list metainfo.AnnounceList, urls metainfo.UrlList, private bool) []byte {
	t.Helper()
	info := metainfo.Info{
		Name:        "test",
		Length:      1024,
		PieceLength: 1024,
		Pieces:      make([]byte, 20),
	}
	if private {
		p := true
		info.Private = &p
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("info marshal: %v", err)
	}
	mi := metainfo.MetaInfo{
		InfoBytes:    infoBytes,
		Announce:     announce,
		AnnounceList: list,
		UrlList:      urls,
		CreationDate: 1000,
		CreatedBy:    "test",
	}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func parseAnnounce(t *testing.T, raw []byte) ([]string, []string) {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return mi.UpvertedAnnounceList().DistinctValues(), []string(mi.UrlList)
}

func TestMergeAddsTrackers(t *testing.T) {
	existing := makeTorrent(t, "", metainfo.AnnounceList{}, nil, false)
	incoming := makeTorrent(t, "http://a/announce",
		metainfo.AnnounceList{{"http://a/announce"}, {"udp://b:80/announce"}},
		nil, false)

	merged, changed, err := mergeTorrent(existing, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	trackers, _ := parseAnnounce(t, merged)
	if len(trackers) != 2 {
		t.Fatalf("expected 2 trackers, got %v", trackers)
	}
}

func TestMergeDedup(t *testing.T) {
	existing := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://a/announce"}, {"udp://b:80/announce"}}, nil, false)
	incoming := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://a/announce"}, {"udp://c:80/announce"}}, nil, false)

	merged, changed, err := mergeTorrent(existing, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (c is new)")
	}
	trackers, _ := parseAnnounce(t, merged)
	if len(trackers) != 3 {
		t.Fatalf("expected 3 distinct trackers, got %v", trackers)
	}
}

func TestMergeNoOpWhenSubset(t *testing.T) {
	existing := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://a/announce"}, {"udp://b:80/announce"}}, nil, false)
	incoming := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://a/announce"}}, nil, false)

	merged, changed, err := mergeTorrent(existing, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false")
	}
	if !bytes.Equal(merged, existing) {
		t.Fatal("expected existing returned verbatim when nothing changed")
	}
}

func TestMergeDefaultTrackersNonPrivate(t *testing.T) {
	existing := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://a/announce"}}, nil, false)
	incoming := existing
	defaults := []string{"udp://open.demonii.com:1337/announce", "http://a/announce"}

	merged, changed, err := mergeTorrent(existing, incoming, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (default tracker added)")
	}
	trackers, _ := parseAnnounce(t, merged)
	if len(trackers) != 2 {
		t.Fatalf("expected 2 trackers (dup filtered), got %v", trackers)
	}
}

func TestMergeDefaultTrackersSkippedOnPrivate(t *testing.T) {
	existing := makeTorrent(t, "",
		metainfo.AnnounceList{{"http://private-tracker/announce"}}, nil, true)
	incoming := existing
	defaults := []string{"udp://open.demonii.com:1337/announce"}

	merged, changed, err := mergeTorrent(existing, incoming, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected no change for private torrent")
	}
	trackers, _ := parseAnnounce(t, merged)
	if len(trackers) != 1 || trackers[0] != "http://private-tracker/announce" {
		t.Fatalf("expected only private tracker, got %v", trackers)
	}
}

func TestMergeUrlList(t *testing.T) {
	existing := makeTorrent(t, "", metainfo.AnnounceList{{"http://a/announce"}},
		metainfo.UrlList{"https://web1/"}, false)
	incoming := makeTorrent(t, "", metainfo.AnnounceList{{"http://a/announce"}},
		metainfo.UrlList{"https://web1/", "https://web2/"}, false)

	merged, changed, err := mergeTorrent(existing, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (url-list grew)")
	}
	_, urls := parseAnnounce(t, merged)
	if len(urls) != 2 {
		t.Fatalf("expected 2 webseeds, got %v", urls)
	}
}

func TestMergePreservesInfoHash(t *testing.T) {
	existing := makeTorrent(t, "", metainfo.AnnounceList{{"http://a/announce"}}, nil, false)
	incoming := makeTorrent(t, "", metainfo.AnnounceList{{"http://b/announce"}}, nil, false)

	exMi, _ := metainfo.Load(bytes.NewReader(existing))
	exHash := exMi.HashInfoBytes()

	merged, _, err := mergeTorrent(existing, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	mergedMi, err := metainfo.Load(bytes.NewReader(merged))
	if err != nil {
		t.Fatal(err)
	}
	if mergedMi.HashInfoBytes() != exHash {
		t.Fatalf("infohash changed: was %v, now %v", exHash, mergedMi.HashInfoBytes())
	}
}

const (
	passkeyA = "http://tracker.invalid/announce?passkey=AAA"
	passkeyB = "http://tracker.invalid/announce?passkey=BBB"
)

// A private torrent's announce URLs carry one user's passkey. A second user's
// upload must not add theirs: the stored copy is served to everyone who pulls
// the infohash. Each case stores the first trackers in a different field.
func TestMergePrivateKeepsFirstTrackers(t *testing.T) {
	incoming := makeTorrent(t, passkeyB, metainfo.AnnounceList{{passkeyB}}, metainfo.UrlList{"https://web.invalid/BBB/"}, true)
	for name, existing := range map[string][]byte{
		"announce":      makeTorrent(t, passkeyA, nil, nil, true),
		"announce-list": makeTorrent(t, "", metainfo.AnnounceList{{passkeyA}}, nil, true),
		"url-list":      makeTorrent(t, "", nil, metainfo.UrlList{"https://web.invalid/AAA/"}, true),
	} {
		t.Run(name, func(t *testing.T) {
			merged, changed, err := mergeTorrent(existing, incoming, []string{"udp://open.invalid:1337/announce"})
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				t.Fatal("expected changed=false: a private torrent keeps its first trackers")
			}
			if !bytes.Equal(merged, existing) {
				t.Fatal("expected existing returned verbatim")
			}
		})
	}
}

// A private torrent stored without any tracker (from a bare magnet) adopts the
// first upload that has some; a trackerless upload still changes nothing.
func TestMergePrivateAdoptsTrackersWhenNoneStored(t *testing.T) {
	existing := makeTorrent(t, "", nil, nil, true)

	same, changed, err := mergeTorrent(existing, makeTorrent(t, "", nil, nil, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(same, existing) {
		t.Fatal("trackerless upload over trackerless copy must change nothing")
	}

	incoming := makeTorrent(t, passkeyB, metainfo.AnnounceList{{passkeyB}}, metainfo.UrlList{"https://web.invalid/BBB/"}, true)
	merged, changed, err := mergeTorrent(existing, incoming, []string{"udp://open.invalid:1337/announce"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true: trackerless private copy adopts incoming trackers")
	}
	trackers, urls := parseAnnounce(t, merged)
	if !slices.Equal(trackers, []string{passkeyB}) || !slices.Equal(urls, []string{"https://web.invalid/BBB/"}) {
		t.Fatalf("expected only incoming trackers and webseeds, got %v %v", trackers, urls)
	}
	exMi, _ := metainfo.Load(bytes.NewReader(existing))
	mergedMi, err := metainfo.Load(bytes.NewReader(merged))
	if err != nil {
		t.Fatal(err)
	}
	if mergedMi.HashInfoBytes() != exMi.HashInfoBytes() {
		t.Fatal("infohash changed")
	}
}

// Copies stored from magnet2torrent before Push dropped the default trackers
// hold only those. They are not the torrent's own trackers: the first real
// upload is adopted, and defaults next to real trackers are cleaned out.
func TestMergePrivateIgnoresDefaultTrackers(t *testing.T) {
	const open = "udp://open.invalid:1337/announce"
	defaults := []string{open}

	merged, changed, err := mergeTorrent(makeTorrent(t, open, metainfo.AnnounceList{{open}}, nil, true),
		makeTorrent(t, passkeyA, metainfo.AnnounceList{{passkeyA}}, nil, true), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if trackers, _ := parseAnnounce(t, merged); !changed || !slices.Equal(trackers, []string{passkeyA}) {
		t.Fatalf("defaults-only copy: changed=%v trackers=%v, want the upload's own", changed, trackers)
	}

	merged, changed, err = mergeTorrent(makeTorrent(t, passkeyA, metainfo.AnnounceList{{passkeyA}, {open}}, nil, true),
		makeTorrent(t, passkeyB, metainfo.AnnounceList{{passkeyB}}, nil, true), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if trackers, _ := parseAnnounce(t, merged); !changed || !slices.Equal(trackers, []string{passkeyA}) {
		t.Fatalf("first trackers plus defaults: changed=%v trackers=%v, want the first only", changed, trackers)
	}
}
