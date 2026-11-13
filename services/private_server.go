package services

import (
	"context"
	"errors"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/webtor-io/torrent-store/privaterecord"
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

// sources are the doors a private Push with Caller may name.
var sources = []string{"site", "rapidapi", "magnet", "vault"}

// recordTiers runs the Caller checks of a private Push and normalizes raw
// (announceTiers of the torrent) into the tiers its record holds. Internal is
// not an account: only a uid owns a record.
func (s *Server) recordTiers(c *pb.Caller, source string, raw [][]string) ([][]string, error) {
	switch {
	case !s.p.Available():
		return nil, refusal(codes.FailedPrecondition, reasonUnavailable, msgUnavailable)
	case c.GetUid() == "":
		return nil, refusal(codes.PermissionDenied, reasonNeedsAccount, msgNeedsAccount)
	case !validID(c.GetUid()) || !slices.Contains(sources, source):
		return nil, status.Error(codes.InvalidArgument, "uid must be 40 hex digits in lower case, source one of site, rapidapi, magnet, vault")
	}
	tiers, err := privaterecord.Normalize(raw)
	switch {
	case errors.Is(err, privaterecord.ErrNoTracker):
		return nil, refusal(codes.InvalidArgument, reasonNoTracker, msgNoTracker)
	case errors.Is(err, privaterecord.ErrTooManyTrackers):
		return nil, refusal(codes.InvalidArgument, reasonTooManyTrackers, msgTooManyTrackers)
	}
	return tiers, err
}

// Bounds of the hosts field. raw is untrusted and not yet normalized, so
// neither the 32-URL nor the 2048-byte cap applies, and a Push may be
// grpcMaxMsgSize: unbounded, one crafted upload would be a multi-MB log line.
// Hosts sees the first maxLogURLs URLs, one past what a record holds; a field
// longer than maxLogHosts bytes is cut and ends in "…". urls counts them all.
const (
	maxLogURLs  = 33
	maxLogHosts = 2048
)

func logHosts(raw [][]string) string {
	var first []string
	for _, t := range raw {
		if len(first) == maxLogURLs {
			break
		}
		first = append(first, t[:min(len(t), maxLogURLs-len(first))]...)
	}
	hosts := strings.Join(privaterecord.Hosts([][]string{first}), ",")
	if len(hosts) > maxLogHosts {
		hosts = strings.ToValidUTF8(hosts[:maxLogHosts], "") + "…"
	}
	return hosts
}

// logRecordPush writes the one private_record_saved line of a private Push
// with Caller. raw is announceTiers before Normalize, so a refusal has hosts
// and urls too: that is how the real maxima get measured. The uid is logged
// only as acct, a URL never, and a source not in sources as "invalid".
func logRecordPush(h, uid, source string, raw [][]string, dry bool, outcome string, err error) {
	urls := 0
	for _, t := range raw {
		urls += len(t)
	}
	if !slices.Contains(sources, source) {
		source = "invalid"
	}
	f := log.Fields{
		"event":    "private_record_saved",
		"acct":     privaterecord.Acct(uid),
		"infohash": h,
		"source":   source,
		"outcome":  outcome,
		"hosts":    logHosts(raw),
		"urls":     urls,
	}
	if dry {
		f["dry_run"] = true
	}
	if err != nil {
		code, reason := status.Code(err), refusalReason(err)
		f["outcome"] = "failed"
		if reason != "" || code == codes.PermissionDenied || code == codes.InvalidArgument {
			f["outcome"] = "refused"
		}
		if reason == "" {
			reason = strings.ToLower(code.String())
		}
		f["reason"] = reason
	}
	if f["outcome"] == "failed" {
		log.WithFields(f).Warn("private record push")
		return
	}
	log.WithFields(f).Info("private record push")
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
