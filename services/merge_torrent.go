package services

import (
	"bytes"

	"github.com/anacrolix/torrent/metainfo"
)

// mergeTorrent unions announce/announce-list/url-list from existing and
// incoming torrents, using existing as the base so creation metadata
// (date, creator, comment) is preserved.
//
// defaultTrackers are appended as an extra tier only when the info dict
// does not have the BEP-27 private flag set — adding open trackers to a
// private torrent gets users banned from the original tracker.
//
// Returns merged bytes and changed=true only when announce-list or
// url-list actually grew. If nothing changed, returns existing as-is.
func mergeTorrent(existing, incoming []byte, defaultTrackers []string) ([]byte, bool, error) {
	exMi, err := metainfo.Load(bytes.NewReader(existing))
	if err != nil {
		return nil, false, err
	}
	inMi, err := metainfo.Load(bytes.NewReader(incoming))
	if err != nil {
		return nil, false, err
	}

	private := false
	if info, err := exMi.UnmarshalInfo(); err == nil && info.Private != nil && *info.Private {
		private = true
	}

	merged := exMi.UpvertedAnnounceList()
	seen := map[string]struct{}{}
	for _, tier := range merged {
		for _, u := range tier {
			seen[u] = struct{}{}
		}
	}

	changed := false
	appendTier := func(urls []string) {
		var tier []string
		for _, u := range urls {
			if u == "" {
				continue
			}
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			tier = append(tier, u)
		}
		if len(tier) > 0 {
			merged = append(merged, tier)
			changed = true
		}
	}

	for _, tier := range inMi.UpvertedAnnounceList() {
		appendTier(tier)
	}
	if !private && len(defaultTrackers) > 0 {
		appendTier(defaultTrackers)
	}

	urlSeen := map[string]struct{}{}
	for _, u := range exMi.UrlList {
		urlSeen[u] = struct{}{}
	}
	mergedUrls := append(metainfo.UrlList(nil), exMi.UrlList...)
	for _, u := range inMi.UrlList {
		if u == "" {
			continue
		}
		if _, ok := urlSeen[u]; ok {
			continue
		}
		urlSeen[u] = struct{}{}
		mergedUrls = append(mergedUrls, u)
		changed = true
	}

	if !changed {
		return existing, false, nil
	}

	exMi.AnnounceList = merged
	if exMi.Announce == "" && len(merged) > 0 && len(merged[0]) > 0 {
		exMi.Announce = merged[0][0]
	}
	exMi.UrlList = mergedUrls

	var buf bytes.Buffer
	if err := exMi.Write(&buf); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}
