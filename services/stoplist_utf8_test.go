package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/webtor-io/torrent-store/proto"
)

// rawInfoTorrent writes a torrent from a hand-built info dict: the metainfo
// struct omits an empty path.utf-8, and that is one of the shapes under test.
func rawInfoTorrent(t *testing.T, info map[string]any) []byte {
	t.Helper()
	info["piece length"] = 1024
	info["pieces"] = make([]byte, 20)
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func oneFile(path, twin []string) map[string]any {
	f := map[string]any{"length": 1, "path": path}
	if twin != nil {
		f["path.utf-8"] = twin
	}
	return f
}

// Legacy encoders put mojibake in name/path and the readable text in the
// .utf-8 twin, which is what Files lists. Two age composites got through on
// the twin alone in October 2026.
func TestStoplistScreensUtf8Twins(t *testing.T) {
	s := mustStoplist(t, noExceptFixture)
	files := func(fs ...map[string]any) []any {
		out := make([]any, len(fs))
		for i, f := range fs {
			out[i] = f
		}
		return out
	}
	cases := []struct {
		name  string
		info  map[string]any
		block bool
	}{
		{"hit in name.utf-8 only", map[string]any{"name": "clean", "name.utf-8": "foo bar", "length": 1}, true},
		{"hit in legacy name only", map[string]any{"name": "foo bar", "name.utf-8": "clean", "length": 1}, true},
		{"hit in path.utf-8 only", map[string]any{"name": "d", "files": files(oneFile([]string{"clean"}, []string{"foo bar"}))}, true},
		{"hit in legacy path, twin differs", map[string]any{"name": "d", "files": files(oneFile([]string{"foo bar"}, []string{"clean"}))}, true},
		// Decodes as a non-nil empty slice; Files then lists the legacy path.
		{"hit in legacy path, twin empty", map[string]any{"name": "d", "files": files(oneFile([]string{"foo bar"}, []string{}))}, true},
		{"clean twins", map[string]any{"name": "clean", "name.utf-8": "also clean", "files": files(oneFile([]string{"a"}, []string{"b"}))}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pt, err := parseTorrent(rawInfoTorrent(t, c.info))
			if err != nil {
				t.Fatal(err)
			}
			cr, byInfo, err := s.CheckParsed(pt)
			if err != nil {
				t.Fatal(err)
			}
			if cr.Found != c.block || byInfo != c.block {
				t.Fatalf("found=%v byInfo=%v, want %v", cr.Found, byInfo, c.block)
			}
		})
	}

	// An equal twin is not screened twice.
	pt, err := parseTorrent(rawInfoTorrent(t, map[string]any{"name": "n", "name.utf-8": "n", "files": files(oneFile([]string{"p"}, []string{"p"}))}))
	if err != nil {
		t.Fatal(err)
	}
	if _, n := s.getData(pt); n != 2 {
		t.Fatalf("info strings = %d, want 2 (name, path)", n)
	}
}

// A manifest cached before the twins were screened carries the bare rule-file
// hash as its stamp. Files must rebuild it, and so refuse, not keep serving it.
func TestFilesRescreensManifestCachedBeforeInputRev(t *testing.T) {
	p := newFakeProvider("mem", true)
	srv := NewServer(NewStore([]StoreProvider{p}), nil, mustStoplist(t, noExceptFixture), nil)
	raw := rawInfoTorrent(t, map[string]any{"name": "clean", "name.utf-8": "foo bar", "length": 1})
	pt, err := parseTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := pt.mi.HashInfoBytes().HexString()
	reply, err := buildManifestParsed(pt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := proto.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(noExceptFixture))
	oldStamp := hex.EncodeToString(sum[:])[:manifestStampLen]
	_, _ = p.Push(context.Background(), h, raw)
	_, _ = p.PushDerived(context.Background(), DerivedManifest, h, append([]byte(oldStamp), b...))

	if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Files: code = %v, want PermissionDenied", status.Code(err))
	}
}
