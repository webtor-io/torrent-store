package services

import (
	"context"
	"testing"

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
