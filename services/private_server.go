package services

import (
	"context"
	"errors"
	"strings"

	log "github.com/sirupsen/logrus"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Refusals of private torrents carry errdetails.ErrorInfo{Domain: errDomain,
// Reason}. A status without it means what it meant before private records.
const (
	errDomain             = "torrent-store"
	reasonUnavailable     = "private_unavailable"
	reasonNeedsAccount    = "private_needs_account"
	reasonNoTracker       = "private_no_tracker"
	reasonTooManyTrackers = "private_too_many_trackers"
	reasonSignIn          = "sign_in"
	reasonNoRecord        = "no_record"
	reasonUnreadable      = "record_unreadable"
)

// The text each refusal is sent with: English, no URLs, no keys.
const (
	msgUnavailable     = "private records need S3 (USE_S3) and PRIVATE_TRACKERS_KEY"
	msgNeedsAccount    = "private torrent needs a signed-in account"
	msgNoTracker       = "private torrent has no usable tracker"
	msgTooManyTrackers = "private torrent has more than 32 trackers"
	msgSignIn          = "private torrent: sign in to play it"
	msgNoRecord        = "private torrent: no tracker record for this account"
	msgUnreadable      = "private record cannot be opened"
)

// refusal is a status with errdetails.ErrorInfo{Domain: errDomain, Reason: reason}.
func refusal(c codes.Code, reason, msg string) error {
	st, err := status.New(c, msg).WithDetails(&errdetails.ErrorInfo{Domain: errDomain, Reason: reason})
	if err != nil { // only for codes.OK; never return a nil status as a refusal
		return status.Error(c, msg)
	}
	return st.Err()
}

// refusalReason is the ErrorInfo.Reason of errDomain, "" if none.
func refusalReason(err error) string {
	for _, d := range status.Convert(err).Details() {
		if e, ok := d.(*errdetails.ErrorInfo); ok && e.GetDomain() == errDomain {
			return e.GetReason()
		}
	}
	return ""
}

// validID is ^[0-9a-f]{40}$: an info hash or a uid. It is the only check of
// their form; Seal and Open do not look at them, and the uid becomes a key prefix.
func validID(s string) bool {
	return len(s) == 40 && strings.Trim(s, "0123456789abcdef") == ""
}

// recordsReady checks ids before anything reaches the bucket, then that records are on.
func (s *Server) recordsReady(ids ...string) error {
	for _, id := range ids {
		if !validID(id) {
			return status.Error(codes.InvalidArgument, "info_hash and uid must be 40 hex digits, uid in lower case")
		}
	}
	if !s.p.Available() {
		return refusal(codes.FailedPrecondition, reasonUnavailable, msgUnavailable)
	}
	return nil
}

// unavailable answers an S3 failure. Neither the status nor the log carries
// the S3 text: a request error names the object URL, and with it the uid.
func unavailable(err error) error {
	code := "unknown"
	var ae interface{ Code() string } // awserr.Error
	if errors.As(err, &ae) {
		code = ae.Code()
	}
	log.WithField("s3_code", code).Warn("private records: S3 request failed")
	return status.Error(codes.Unavailable, "private records: S3 request failed")
}

func (s *Server) GetPrivateRecord(ctx context.Context, in *pb.GetPrivateRecordRequest) (*pb.GetPrivateRecordReply, error) {
	h, uid := strings.ToLower(in.GetInfoHash()), in.GetUid()
	if err := s.recordsReady(h, uid); err != nil {
		return nil, err
	}
	_, tiers, err := s.p.open(ctx, h, uid)
	switch {
	case errors.Is(err, ErrNotFound):
		return &pb.GetPrivateRecordReply{}, nil
	case errors.Is(err, errUnreadable):
		return nil, refusal(codes.DataLoss, reasonUnreadable, msgUnreadable)
	case err != nil:
		return nil, unavailable(err)
	}
	r := &pb.GetPrivateRecordReply{Found: true}
	for _, t := range tiers {
		r.Tiers = append(r.Tiers, &pb.Tier{Urls: t})
	}
	return r, nil
}

func (s *Server) ListPrivateRecords(ctx context.Context, in *pb.ListPrivateRecordsRequest) (*pb.ListPrivateRecordsReply, error) {
	if err := s.recordsReady(in.GetUid()); err != nil {
		return nil, err
	}
	recs, err := s.p.list(ctx, in.GetUid())
	if err != nil {
		return nil, unavailable(err)
	}
	return &pb.ListPrivateRecordsReply{Records: recs}, nil
}

func (s *Server) DeletePrivateRecord(ctx context.Context, in *pb.DeletePrivateRecordRequest) (*pb.DeletePrivateRecordReply, error) {
	h, uid := strings.ToLower(in.GetInfoHash()), in.GetUid()
	if err := s.recordsReady(h, uid); err != nil {
		return nil, err
	}
	ok, err := s.p.delete(ctx, h, uid)
	if err != nil {
		return nil, unavailable(err)
	}
	return &pb.DeletePrivateRecordReply{Deleted: ok}, nil
}

func (s *Server) DeletePrivateRecordsByUser(ctx context.Context, in *pb.DeletePrivateRecordsByUserRequest) (*pb.DeletePrivateRecordsReply, error) {
	if err := s.recordsReady(in.GetUid()); err != nil {
		return nil, err
	}
	n, err := s.p.deleteByUser(ctx, in.GetUid())
	if err != nil {
		return nil, unavailable(err)
	}
	return &pb.DeletePrivateRecordsReply{Deleted: n}, nil
}
