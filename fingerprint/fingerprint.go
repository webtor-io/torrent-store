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
// Two kinds are emitted, and they differ in how much they can promise:
//
//   - FingerprintLayout (v1): SHA-256 over the piece geometry and the full
//     piece-hash table. Piece hashes are computed over the CONCATENATION of
//     all files, so this identifies the torrent's whole payload-plus-layout.
//     It matches an exact re-wrap — same files, same order, same sizes, same
//     piece length — which is the common republishing pattern. It does NOT
//     survive repacking: changing the size of any file shifts every later byte
//     across piece boundaries and changes every subsequent piece hash. For a
//     single-file torrent there is nothing to shift, so the layout
//     fingerprint is a true content identity for that file.
//
//   - FingerprintFile (v2, BEP 52): the per-file merkle root taken straight
//     from the file tree. This is a real position-independent identity for one
//     file's bytes and survives repacking, but only v2 (or hybrid) torrents
//     carry it.
//
// Deliberately NOT attempted: per-file fingerprints for v1 by hashing the
// pieces that fall inside a file's byte range. Those pieces are only stable
// while the file's offset is stable, so the result would look like a per-file
// identity while silently behaving like a layout one.
const (
	FingerprintLayout = "v1layout"
	FingerprintFile   = "v2file"
)

// minFingerprintFileLength skips files too small to be worth identifying.
// Small files travel with unrelated payloads — a README, a tracker blurb, a
// cover image — and fingerprinting them would link torrents that share nothing
// that matters.
const minFingerprintFileLength = 1 << 20 // 1 MiB

// Fingerprint is one content identity derived from a torrent.
type Fingerprint struct {
	// Kind is FingerprintLayout or FingerprintFile.
	Kind string
	// Value is the hex digest.
	Value string
	// Length is the number of payload bytes this fingerprint covers: the
	// torrent's total length for a layout fingerprint, the file's length for
	// a file fingerprint. Carried for diagnostics — matching is on Kind+Value.
	Length int64
}

// String renders a fingerprint in the "kind:hex" form used for storage keys.
func (f Fingerprint) String() string { return f.Kind + ":" + f.Value }

// Compute derives every content identity available from a .torrent.
//
// A torrent always yields at least the layout fingerprint. v2 and hybrid
// torrents additionally yield one file fingerprint per file over the size
// threshold. The result is deterministic and ordered: layout first, then file
// fingerprints sorted by the order the file tree walks.
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
		// Domain-separate so a layout digest can never collide with a file
		// digest, and length-prefix the geometry so it cannot be confused
		// with the piece table that follows.
		h.Write([]byte(FingerprintLayout))
		_ = binary.Write(h, binary.BigEndian, info.PieceLength)
		_ = binary.Write(h, binary.BigEndian, total)
		h.Write(info.Pieces)
		res = append(res, Fingerprint{
			Kind:   FingerprintLayout,
			Value:  hex.EncodeToString(h.Sum(nil)),
			Length: total,
		})
	}

	if info.HasV2() {
		info.FileTree.Walk(nil, func(_ []string, ft *metainfo.FileTree) {
			root := ft.PiecesRootAsByteArray()
			if !root.Ok || ft.File.Length < minFingerprintFileLength {
				return
			}
			h := sha256.New()
			h.Write([]byte(FingerprintFile))
			_ = binary.Write(h, binary.BigEndian, ft.File.Length)
			h.Write(root.Value[:])
			res = append(res, Fingerprint{
				Kind:   FingerprintFile,
				Value:  hex.EncodeToString(h.Sum(nil)),
				Length: ft.File.Length,
			})
		})
	}

	if len(res) == 0 {
		return nil, errors.New("torrent yielded no fingerprints")
	}
	return res, nil
}
