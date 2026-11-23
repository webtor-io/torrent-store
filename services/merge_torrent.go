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
// passkey, and each user's trackers live in their own record. Its stored copy
// is info-only: whatever announce, announce-list or url-list a copy stored
// before records still has (a first uploader's passkey, or the defaults
// magnet2torrent left) is cleared, and incoming lists are never taken.
//
// Returns merged bytes and changed=true only when announce-list or url-list
// actually grew, or a private copy had lists to clear. If nothing changed,
// returns existing as-is.
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
		if !hasTrackers(exMi) {
			return existing, false, nil
		}
		b, err := infoOnly(exMi)
		return b, err == nil, err
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

// infoOnly is mi without Announce, AnnounceList and UrlList; InfoBytes, and
// with them the infohash, are untouched.
func infoOnly(mi *metainfo.MetaInfo) ([]byte, error) { return withTiers(mi, nil) }

// StripPrivate is what private-rewrite stores in place of raw. private is
// the BEP 27 flag. For a private torrent that names a tracker or webseed, out
// is infoOnly of it and hash the infohash read back from out; out is nil for
// every other torrent.
func StripPrivate(raw []byte) (out []byte, hash string, private bool, err error) {
	pt, err := parseTorrent(raw)
	if err != nil {
		return nil, "", false, err
	}
	private = pt.info.Private != nil && *pt.info.Private
	if !private || !hasTrackers(pt.mi) {
		return nil, "", private, nil
	}
	if out, err = infoOnly(pt.mi); err != nil {
		return nil, "", true, err
	}
	mi, err := metainfo.Load(bytes.NewReader(out))
	if err != nil {
		return nil, "", true, err
	}
	return out, mi.HashInfoBytes().HexString(), true, nil
}

// withTiers is infoOnly with tiers as the announce-list and their first URL
// as announce: nothing of mi's own trackers or webseeds is left.
func withTiers(mi *metainfo.MetaInfo, tiers [][]string) ([]byte, error) {
	c := *mi
	c.Announce, c.AnnounceList, c.UrlList = "", tiers, nil
	if len(tiers) > 0 && len(tiers[0]) > 0 {
		c.Announce = tiers[0][0]
	}
	var buf bytes.Buffer
	if err := c.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// announceTiers is what a private record is made of before Normalize: mi's
// UpvertedAnnounceList without defaults, dropped from a copy so mi is unchanged.
func announceTiers(mi *metainfo.MetaInfo, defaults []string) [][]string {
	c := *mi
	dropTrackers(&c, defaults)
	return c.UpvertedAnnounceList()
}
