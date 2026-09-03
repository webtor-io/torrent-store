package services

import (
	"strings"

	pb "github.com/webtor-io/torrent-store/proto"
)

// buildManifest parses a .torrent into the lightweight file manifest used
// for listing: the torrent name plus each file's full path (name-prefixed,
// matching the rest-api convention) and size. Piece hashes are dropped —
// they aren't needed for listing and dominate the .torrent size.
func buildManifest(torrent []byte) (*pb.FilesReply, error) {
	pt, err := parseTorrent(torrent)
	if err != nil {
		return nil, err
	}
	return buildManifestParsed(pt)
}

// buildManifestParsed is buildManifest for callers that already hold a parse.
func buildManifestParsed(pt *parsedTorrent) (*pb.FilesReply, error) {
	info := &pt.info
	name := info.Name
	if info.NameUtf8 != "" {
		name = info.NameUtf8
	}
	name = validUTF8(name)
	reply := &pb.FilesReply{Name: name}
	for _, f := range info.UpvertedFiles() {
		path := f.Path
		if len(f.PathUtf8) > 0 {
			path = f.PathUtf8
		}
		full := make([]string, 0, len(path)+1)
		full = append(full, name)
		for _, p := range path {
			full = append(full, validUTF8(p))
		}
		reply.Files = append(reply.Files, &pb.FileInfo{
			Path:   full,
			Length: f.Length,
		})
	}
	return reply, nil
}

// validUTF8 replaces bytes that are not valid UTF-8 with U+FFFD. Torrents
// made by legacy encoders carry GBK / Shift-JIS names without the *.utf-8
// fields; proto3 string fields reject such bytes, so without this the
// whole Files reply fails to marshal and the torrent becomes unlistable.
// The readable ASCII part of the name survives, which is what listing needs.
func validUTF8(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
