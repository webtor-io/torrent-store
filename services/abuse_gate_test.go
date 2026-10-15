package services

import (
	"context"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAbuseGateMatrix pins the per-RPC blocking matrix documented in
// abuse_gate.go. Every row there is a decision; an RPC dropping out of its
// row must turn this test red rather than wait for an audit to notice.
func TestAbuseGateMatrix(t *testing.T) {
	t.Run("banned infohash", func(t *testing.T) {
		a := &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{}}
		srv, h, raw := serverWithTorrent(t, a)
		a.byHash[h] = true

		if _, err := srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Pull: code = %v, want PermissionDenied", status.Code(err))
		}
		if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Push: code = %v, want PermissionDenied", status.Code(err))
		}
		if _, err := srv.Files(context.Background(), &pb.FilesRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Files: code = %v, want PermissionDenied", status.Code(err))
		}
		if _, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Fingerprint: code = %v, want PermissionDenied", status.Code(err))
		}
		// Touch is ungated by design: it only refreshes the storage TTL, and
		// serving is gated at Pull/Files.
		if _, err := srv.Touch(context.Background(), &pb.TouchRequest{InfoHash: h}); err != nil {
			t.Fatalf("Touch must stay ungated, got %v", err)
		}
	})

	t.Run("banned payload under another infohash", func(t *testing.T) {
		a := &fakeAbuse{byHash: map[string]bool{}, byDigest: map[string]bool{}}
		srv, h, raw := serverWithTorrent(t, a)
		a.byDigest[digestOf(t, raw)] = true

		// Pull and Push hold the torrent bytes, so both refuse outright. The
		// cold/warm behaviour of Files is pinned separately by
		// TestPayloadBlockedUnderAnotherInfohash.
		if _, err := srv.Pull(context.Background(), &pb.PullRequest{InfoHash: h}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Pull: code = %v, want PermissionDenied", status.Code(err))
		}
		if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Push: code = %v, want PermissionDenied", status.Code(err))
		}
		// Fingerprint deliberately still answers: its consumers are the abuse
		// tooling, and the fingerprint of a payload-blocked torrent is exactly
		// what they come to fetch.
		if _, err := srv.Fingerprint(context.Background(), &pb.FingerprintRequest{InfoHash: h}); err != nil {
			t.Fatalf("Fingerprint must serve a payload-blocked torrent, got %v", err)
		}
		if _, err := srv.Touch(context.Background(), &pb.TouchRequest{InfoHash: h}); err != nil {
			t.Fatalf("Touch must stay ungated, got %v", err)
		}
	})
}

// A torrent the stoplist refused at Push is never stored, so Touch used to
// answer NotFound and the caller resolved the same magnet again, only to be
// refused again. Touch now refuses it too — but only under the rules that
// refused it.
func TestTouchRemembersStoplistRefusal(t *testing.T) {
	srv := NewServer(NewStore([]StoreProvider{newFakeProvider("mem", true)}), nil, mustStoplist(t, noExceptFixture), nil)
	raw := makeRawTorrent(t, metainfo.Info{Name: "foo bar", Length: 1500, PieceLength: 1024, Pieces: make([]byte, 2*20)})
	pt, err := parseTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := pt.mi.HashInfoBytes().HexString()
	touch := func() codes.Code {
		_, err := srv.Touch(context.Background(), &pb.TouchRequest{InfoHash: h})
		return status.Code(err)
	}

	if c := touch(); c != codes.NotFound {
		t.Fatalf("Touch before any Push: code = %v, want NotFound", c)
	}
	if _, err := srv.Push(context.Background(), &pb.PushRequest{Torrent: raw}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Push: code = %v, want PermissionDenied", status.Code(err))
	}
	if c := touch(); c != codes.PermissionDenied {
		t.Fatalf("Touch after the refusal: code = %v, want PermissionDenied", c)
	}
	// Different rules, different version: the refusal no longer stands.
	srv.sl = mustStoplist(t, exceptFixture)
	if c := touch(); c != codes.NotFound {
		t.Fatalf("Touch under another stoplist version: code = %v, want NotFound", c)
	}
}
