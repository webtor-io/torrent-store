package services

import (
	"bytes"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
)

// parsedTorrent is one .torrent parsed once and handed to every check that
// needs it. Before it existed each consumer did its own metainfo.Load +
// UnmarshalInfo, so a single Pull re-parsed the same bytes three times
// (stoplist, geometry, fingerprint) and a Push four.
type parsedTorrent struct {
	raw  []byte
	mi   *metainfo.MetaInfo
	info metainfo.Info
}

func parseTorrent(raw []byte) (*parsedTorrent, error) {
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.Wrap(err, "failed to load torrent")
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal info")
	}
	return &parsedTorrent{raw: raw, mi: mi, info: info}, nil
}
