package services

import (
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
	reply := &pb.FilesReply{Name: name}
	for _, f := range info.UpvertedFiles() {
		path := f.Path
		if len(f.PathUtf8) > 0 {
			path = f.PathUtf8
		}
		full := append([]string{name}, path...)
		reply.Files = append(reply.Files, &pb.FileInfo{
			Path:   full,
			Length: f.Length,
		})
	}
	return reply, nil
}
