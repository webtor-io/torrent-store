package fingerprint

import (
	"bytes"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// encodeTorrent bencodes an Info into a full .torrent so tests exercise the
// same parse path production does.
func encodeTorrent(t *testing.T, info metainfo.Info) []byte {
	t.Helper()
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	var buf bytes.Buffer
	if err := bencode.NewEncoder(&buf).Encode(metainfo.MetaInfo{InfoBytes: infoBytes}); err != nil {
		t.Fatalf("encode metainfo: %v", err)
	}
	return buf.Bytes()
}

// pieceTable builds a deterministic but distinct-looking piece table.
func pieceTable(n int, seed byte) []byte {
	p := make([]byte, n*20)
	for i := range p {
		p[i] = seed ^ byte(i%251)
	}
	return p
}

func layoutOf(t *testing.T, torrent []byte) string {
	t.Helper()
	fps, err := Compute(torrent)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	for _, f := range fps {
		if f.Kind == FingerprintLayout {
			return f.Value
		}
	}
	t.Fatal("no layout fingerprint returned")
	return ""
}

// The case this whole mechanism exists for: one payload republished under many
// torrent names. Different name means a different infohash, but the bytes are
// the same, so the fingerprint has to match.
func TestLayoutFingerprintIgnoresTorrentName(t *testing.T) {
	pieces := pieceTable(868, 0x5a)
	mk := func(name string) []byte {
		return encodeTorrent(t, metainfo.Info{
			Name:        name,
			PieceLength: 262144,
			Pieces:      pieces,
			Files: []metainfo.FileInfo{
				{Path: []string{"Links.txt"}, Length: 271},
				{Path: []string{"Preview Video.mp4"}, Length: 227432058},
			},
		})
	}
	a := layoutOf(t, mk("2TB VIP VIDEO LINKS Teen Solo Teen Socks"))
	b := layoutOf(t, mk("55GiB NEW pack fresh links"))
	c := layoutOf(t, mk("completely unrelated wording"))
	if a != b || b != c {
		t.Fatalf("same payload under different names produced different fingerprints:\n %s\n %s\n %s", a, b, c)
	}
}

// Different payloads must not collide, including when only the piece table
// differs (same geometry, different content) — that is the sibling-campaign
// case, where two sets share a shape but not the bytes.
func TestLayoutFingerprintSeparatesDifferentPayloads(t *testing.T) {
	base := metainfo.Info{
		Name:        "same name",
		PieceLength: 262144,
		Pieces:      pieceTable(868, 0x5a),
		Files: []metainfo.FileInfo{
			{Path: []string{"Links.txt"}, Length: 271},
			{Path: []string{"Preview Video.mp4"}, Length: 227432058},
		},
	}
	a := layoutOf(t, encodeTorrent(t, base))

	other := base
	other.Pieces = pieceTable(868, 0x77) // same geometry, different content
	b := layoutOf(t, encodeTorrent(t, other))

	shorter := base
	shorter.Pieces = pieceTable(891, 0x5a) // different length payload
	shorter.Files = []metainfo.FileInfo{
		{Path: []string{"Links.txt"}, Length: 173},
		{Path: []string{"Preview.mp4"}, Length: 233551143},
	}
	c := layoutOf(t, encodeTorrent(t, shorter))

	geom := base
	geom.PieceLength = 524288 // same bytes, different piece length
	d := layoutOf(t, encodeTorrent(t, geom))

	for i, pair := range [][2]string{{a, b}, {a, c}, {a, d}, {b, c}} {
		if pair[0] == pair[1] {
			t.Fatalf("case %d: distinct payloads shared a fingerprint %s", i, pair[0])
		}
	}
}

// A single-file torrent has nothing that can shift, so its layout fingerprint
// is a true identity for that file and must survive renaming the torrent.
func TestLayoutFingerprintStableForSingleFile(t *testing.T) {
	pieces := pieceTable(64, 0x11)
	mk := func(name string) []byte {
		return encodeTorrent(t, metainfo.Info{
			Name: name, Length: 16 << 20, PieceLength: 262144, Pieces: pieces,
		})
	}
	if layoutOf(t, mk("film.2021.1080p")) != layoutOf(t, mk("totally different name")) {
		t.Fatal("single-file layout fingerprint changed with the torrent name")
	}
}

// Documents the known limit honestly: repacking with a different sibling size
// shifts every later byte across piece boundaries, so v1 cannot follow it.
// If this ever starts passing, v1 gained a property it does not have.
func TestLayoutFingerprintDoesNotSurviveRepack(t *testing.T) {
	mk := func(sidecar int64, pieces []byte) []byte {
		return encodeTorrent(t, metainfo.Info{
			Name: "n", PieceLength: 262144, Pieces: pieces,
			Files: []metainfo.FileInfo{
				{Path: []string{"Links.txt"}, Length: sidecar},
				{Path: []string{"Preview.mp4"}, Length: 227432058},
			},
		})
	}
	// A different sidecar size necessarily reshuffles the piece boundaries,
	// which in a real torrent means a different piece table.
	a := layoutOf(t, mk(271, pieceTable(868, 0x5a)))
	b := layoutOf(t, mk(999, pieceTable(868, 0x5b)))
	if a == b {
		t.Fatal("expected repacked torrent to fingerprint differently under v1")
	}
}

func TestFingerprintsRejectsUnusableInput(t *testing.T) {
	if _, err := Compute([]byte("not a torrent")); err == nil {
		t.Fatal("expected an error for malformed input")
	}
	// Pieces present but no geometry: refuse rather than emit a digest over
	// an inconsistent info dict.
	bad := encodeTorrent(t, metainfo.Info{Name: "n", Length: 10, Pieces: make([]byte, 20)})
	if _, err := Compute(bad); err == nil {
		t.Fatal("expected an error when piece length is missing")
	}
}

func TestFingerprintString(t *testing.T) {
	f := Fingerprint{Kind: FingerprintLayout, Value: "abc123"}
	if got, want := f.String(), "v1layout:abc123"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
