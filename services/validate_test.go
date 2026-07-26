package services

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateInfoGeometry(t *testing.T) {
	cases := []struct {
		name    string
		info    metainfo.Info
		wantErr string
	}{
		{
			name: "valid single file exact piece",
			info: metainfo.Info{Name: "a", Length: 1024, PieceLength: 1024, Pieces: make([]byte, 20)},
		},
		{
			name: "valid multi file with short last piece",
			info: metainfo.Info{
				Name:        "d",
				PieceLength: 1024,
				Pieces:      make([]byte, 3*20),
				Files: []metainfo.FileInfo{
					{Path: []string{"x"}, Length: 2000},
					{Path: []string{"y"}, Length: 49},
				},
			},
		},
		{
			name:    "zero piece length",
			info:    metainfo.Info{Name: "a", Length: 1024, Pieces: make([]byte, 20)},
			wantErr: "piece length",
		},
		{
			name:    "no pieces",
			info:    metainfo.Info{Name: "a", Length: 1024, PieceLength: 1024},
			wantErr: "no v1 pieces",
		},
		{
			name:    "pieces not multiple of 20",
			info:    metainfo.Info{Name: "a", Length: 1024, PieceLength: 1024, Pieces: make([]byte, 21)},
			wantErr: "multiple of 20",
		},
		{
			// The 2026-07-25 seeder crash shape: files claim far more bytes
			// than the pieces blob covers, so the computed last-piece length
			// exceeds the piece length and V1Length panics downstream.
			name:    "files exceed piece space",
			info:    metainfo.Info{Name: "a", Length: 10 * 1024, PieceLength: 1024, Pieces: make([]byte, 2*20)},
			wantErr: "geometry mismatch",
		},
		{
			name:    "too many pieces for files",
			info:    metainfo.Info{Name: "a", Length: 1024, PieceLength: 1024, Pieces: make([]byte, 3*20)},
			wantErr: "geometry mismatch",
		},
		{
			name: "negative file length",
			info: metainfo.Info{
				Name:        "d",
				PieceLength: 1024,
				Pieces:      make([]byte, 20),
				Files:       []metainfo.FileInfo{{Path: []string{"x"}, Length: -5}},
			},
			wantErr: "negative length",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInfoGeometry(&tc.info)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func makeRawTorrent(t *testing.T, info metainfo.Info) []byte {
	t.Helper()
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("info marshal: %v", err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func malformedInfo() metainfo.Info {
	return metainfo.Info{Name: "bad", Length: 10 * 1024, PieceLength: 1024, Pieces: make([]byte, 2*20)}
}

func validInfo() metainfo.Info {
	return metainfo.Info{Name: "good", Length: 1500, PieceLength: 1024, Pieces: make([]byte, 2*20)}
}

func TestPushRejectsMalformedTorrent(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	_, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeRawTorrent(t, malformedInfo())})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err %v), want InvalidArgument", status.Code(err), err)
	}
}

func TestPushAcceptsValidTorrent(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, nil, nil)
	reply, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: makeRawTorrent(t, validInfo())})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if reply.GetInfoHash() == "" {
		t.Fatal("empty infohash")
	}
}

func TestPullRejectsStoredMalformedTorrent(t *testing.T) {
	// Malformed torrent already in a provider, as if stored before Push
	// validation existed.
	raw := makeRawTorrent(t, malformedInfo())
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := mi.HashInfoBytes().HexString()
	p := newFakeProvider("mem", true)
	p.torrents[h] = raw

	srv := NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)
	_, err = srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err %v), want FailedPrecondition", status.Code(err), err)
	}
}
