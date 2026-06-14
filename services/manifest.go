package services

import (
	"bytes"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	pb "github.com/webtor-io/torrent-store/proto"
)

// buildManifest parses a .torrent into the lightweight file manifest used
// for listing: the torrent name plus each file's full path (name-prefixed,
// matching the rest-api convention) and size. Piece hashes are dropped —
// they aren't needed for listing and dominate the .torrent size.
func buildManifest(torrent []byte) (*pb.FilesReply, error) {
	mi, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		return nil, errors.Wrap(err, "failed to load torrent")
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal info")
	}
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
