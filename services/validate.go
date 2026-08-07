package services

import (
	"fmt"

	"github.com/anacrolix/torrent/metainfo"
)

// ValidateInfoGeometry checks that the v1 piece geometry is consistent with
// the declared file lengths: the pieces blob must describe exactly the byte
// range the files occupy. anacrolix-based consumers (torrent-web-seeder)
// trust this invariant and panic inside metainfo.Piece.V1Length when it is
// violated, taking the whole pod down, so a torrent that fails this check
// can never be served and must be refused.
// maxPieceLength caps the declared piece size. The largest value seen in the
// wild is 128 MiB; the cap sits at double that so no real torrent is refused.
// It must stay well under math.MaxInt64/numPieces for any plausible piece
// count: a crafted PieceLength like 1<<62 overflows the (numPieces-1)*
// PieceLength product below, wraps the computed last-piece length back into
// the "valid" range, and lets nonsense geometry through the very check that
// exists to keep it out.
const maxPieceLength = 256 << 20

func ValidateInfoGeometry(info *metainfo.Info) error {
	if info.PieceLength <= 0 {
		return fmt.Errorf("piece length must be positive, got %d", info.PieceLength)
	}
	if info.PieceLength > maxPieceLength {
		return fmt.Errorf("piece length %d exceeds the %d cap", info.PieceLength, int64(maxPieceLength))
	}
	if len(info.Pieces) == 0 {
		return fmt.Errorf("no v1 pieces")
	}
	if len(info.Pieces)%20 != 0 {
		return fmt.Errorf("pieces length %d is not a multiple of 20", len(info.Pieces))
	}
	var total int64
	for i, f := range info.UpvertedV1Files() {
		if f.Length < 0 {
			return fmt.Errorf("file %d has negative length %d", i, f.Length)
		}
		prev := total
		total += f.Length
		if total < prev {
			return fmt.Errorf("total file length overflows int64")
		}
	}
	numPieces := int64(len(info.Pieces) / 20)
	lastPieceLength := total - (numPieces-1)*info.PieceLength
	if lastPieceLength <= 0 || lastPieceLength > info.PieceLength {
		return fmt.Errorf("piece geometry mismatch: %d bytes of files vs %d pieces of %d bytes", total, numPieces, info.PieceLength)
	}
	return nil
}
