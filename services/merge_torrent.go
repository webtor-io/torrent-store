package services

import (
	"bytes"
	"slices"

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
// A private torrent is never unioned: its announce URLs carry one user's
// passkey, and a union handed every user's passkey to everyone who pulls the
// infohash. It keeps the first trackers it was stored with; a later upload is
// adopted only while the stored copy has none at all (stored from a bare
// magnet; defaultTrackers don't count and are cleaned out). Replacing instead
// would not help: a running seeder only ever adds trackers, so it would
// announce both passkeys anyway.
//
// Returns merged bytes and changed=true only when announce-list or url-list
// actually grew, or a private copy was cleaned or adopted trackers. If nothing
// changed, returns existing as-is.
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
	if private {
		// ponytail: a revoked first passkey is never healed by a later
		// upload; healing needs a signal that the tracker rejects it, and
		// nothing reports one today.
		//
		// Default trackers are never a private torrent's own: copies stored
		// from magnet2torrent before Push dropped them hold nothing else, and
		// counting them would lock such a copy out of its real trackers.
		// Push has already dropped them from incoming.
		cleaned := dropTrackers(exMi, defaultTrackers)
		if !hasTrackers(exMi) && hasTrackers(inMi) {
			exMi.Announce, exMi.AnnounceList, exMi.UrlList = inMi.Announce, inMi.AnnounceList, inMi.UrlList
		} else if !cleaned {
			return existing, false, nil
		}
		var buf bytes.Buffer
		if err := exMi.Write(&buf); err != nil {
			return nil, false, err
		}
		return buf.Bytes(), true, nil
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
	if len(defaultTrackers) > 0 {
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

// hasTrackers reports whether mi names any tracker or webseed.
func hasTrackers(mi *metainfo.MetaInfo) bool {
	nonEmpty := func(u string) bool { return u != "" }
	return mi.Announce != "" ||
		slices.ContainsFunc(mi.AnnounceList, func(tier []string) bool { return slices.ContainsFunc(tier, nonEmpty) }) ||
		slices.ContainsFunc(mi.UrlList, nonEmpty)
}

// dropTrackers removes the given URLs from mi's announce and announce-list
// and reports whether any matched. magnet2torrent announces DEFAULT_TRACKERS
// while resolving a magnet and leaves them in the metainfo it returns; they
// must never be stored or announced for a private torrent.
func dropTrackers(mi *metainfo.MetaInfo, drop []string) bool {
	dropped := false
	keep := func(u string) bool {
		if slices.Contains(drop, u) {
			dropped = true
			return false
		}
		return true
	}
	var list metainfo.AnnounceList
	for _, tier := range mi.AnnounceList {
		var kept []string
		for _, u := range tier {
			if keep(u) {
				kept = append(kept, u)
			}
		}
		if len(kept) > 0 {
			list = append(list, kept)
		}
	}
	if !keep(mi.Announce) {
		mi.Announce = ""
	}
	if dropped {
		mi.AnnounceList = list
	}
	return dropped
}
