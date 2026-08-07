package fingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
)

// Fingerprints identify a torrent by its CONTENT rather than by its infohash.
//
// The infohash covers the whole info dict, so re-wrapping the same payload
// under a different torrent name yields a different infohash. Republishing one
// payload under many names is therefore invisible to per-infohash blocking:
// every copy has to be found and blocked again by hand. A content fingerprint
// collapses the whole set to a single decision — block one copy, and every
// other copy of the same bytes is known too, including ones uploaded later.
//
// One kind is emitted today:
//
//   - v1 layout: SHA-256 over the piece geometry and the full
//     piece-hash table. Piece hashes are computed over the CONCATENATION of
//     all files, so this identifies the torrent's whole payload-plus-layout.
//     It matches an exact re-wrap — same files, same order, same sizes, same
//     piece length — which is the common republishing pattern. It does NOT
//     survive repacking: changing the size of any file shifts every later byte
//     across piece boundaries and changes every subsequent piece hash. For a
//     single-file torrent there is nothing to shift, so the layout
//     fingerprint is a true content identity for that file.
//
// Deliberately NOT attempted: per-file fingerprints for v1 by hashing the
// pieces that fall inside a file's byte range. Those pieces are only stable
// while the file's offset is stable, so the result would look like a per-file
// identity while silently behaving like a layout one.
//
// BEP 52 per-file merkle roots were implemented and then removed. They are a
// genuine position-independent identity, but v2 torrents were 1 in 60 of a
// live sample, so the branch carried ~2% of the value while producing 100% of
// this package's defects — a panic and a non-determinism, both only reachable
// through it. Two things for whoever adds it back:
//   - metainfo's PiecesRootAsByteArray PANICS when a root is present but not
//     exactly 32 bytes, and torrents come from callers. Check the length first.
//   - FileTree.Walk descends a map, so its output order is randomised and must
//     be sorted before it reaches storage.
//
// It also matches at FILE granularity rather than whole-torrent, so a ban
// propagating through it reaches every torrent containing that file — a wider
// blast radius than v1layout, and worth deciding on deliberately.

// Fingerprint is one content identity derived from a torrent.
type Fingerprint struct {
	// Value is the hex digest.
	Value string
	// Length is the number of payload bytes this fingerprint covers: the
	// torrent's total length for a layout fingerprint, the file's length for
	// a file fingerprint. Carried for diagnostics — matching is on Value alone.
	Length int64
}

// String renders a fingerprint as its hex digest.
func (f Fingerprint) String() string { return f.Value }

// Compute derives every content identity available from a .torrent.
//
// Every torrent with a v1 piece table yields exactly one layout fingerprint.
func Compute(torrent []byte) ([]Fingerprint, error) {
	mi, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		return nil, errors.Wrap(err, "failed to load torrent")
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal info")
	}
	return fingerprintsFromInfo(&info)
}

func fingerprintsFromInfo(info *metainfo.Info) ([]Fingerprint, error) {
	var res []Fingerprint

	if len(info.Pieces) > 0 {
		if info.PieceLength <= 0 {
			return nil, errors.New("torrent has piece hashes but no piece length")
		}
		total := info.TotalLength()
		h := sha256.New()
		// Length-prefix the geometry so it cannot be confused with the piece
		// table that follows.
		_ = binary.Write(h, binary.BigEndian, info.PieceLength)
		_ = binary.Write(h, binary.BigEndian, total)
		h.Write(info.Pieces)
		res = append(res, Fingerprint{
			Value:  hex.EncodeToString(h.Sum(nil)),
			Length: total,
		})
	}

	if len(res) == 0 {
		return nil, errors.New("torrent yielded no fingerprints")
	}
	return res, nil
}
