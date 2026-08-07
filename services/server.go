package services

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const defaultTrackersFlag = "default-trackers"

// RegisterServerFlags adds the default-trackers flag used by Server to
// inject extra trackers into pushed torrents (respecting BEP-27 private).
func RegisterServerFlags(f []cli.Flag) []cli.Flag {
	return append(f, cli.StringFlag{
		Name:   defaultTrackersFlag,
		Usage:  "comma-separated tracker URLs appended to non-private torrents on Push (dedup'd against existing)",
		Value:  "",
		EnvVar: "DEFAULT_TRACKERS",
	})
}

// ParseDefaultTrackers reads --default-trackers (comma- or whitespace-separated) into a slice.
func ParseDefaultTrackers(c *cli.Context) []string {
	raw := c.String(defaultTrackersFlag)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// abuseChecker is the blocking oracle. An interface so the decision can be
// exercised in tests without standing up a gRPC client; *Abuse is the only
// production implementation.
type abuseChecker interface {
	Get(ctx context.Context, h string) (bool, error)
	CheckFingerprint(ctx context.Context, fp []byte) (bool, error)
}

type Server struct {
	pb.UnimplementedTorrentStoreServer
	s               *Store
	a               abuseChecker
	sl              *Stoplist
	defaultTrackers []string
}

func NewServer(s *Store, a *Abuse, sl *Stoplist, defaultTrackers []string) *Server {
	srv := &Server{
		s:               s,
		sl:              sl,
		defaultTrackers: defaultTrackers,
	}
	// Assign only when non-nil. NewAbuse returns a nil *Abuse when abuse
	// checking is off, and storing that straight into an interface would give
	// a non-nil interface holding a nil pointer — every `s.a == nil` guard
	// would then pass and the first call would dereference nil.
	if a != nil {
		srv.a = a
	}
	return srv
}

func (s *Server) Pull(ctx context.Context, in *pb.PullRequest) (*pb.PullReply, error) {
	t := time.Now()

	hLog := log.WithField("infoHash", in.GetInfoHash()).WithField("method", "pull")
	hLog.Info("pull torrent request")

	abused, err := s.isAbused(ctx, in.GetInfoHash())
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", in.GetInfoHash())
	}
	if abused {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", in.GetInfoHash())
	}
	torrent, err := s.s.Pull(ctx, in.GetInfoHash())
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return nil, status.Errorf(codes.NotFound, "unable to find torrent for infoHash=%v", in.GetInfoHash())
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to pull")
		return nil, errors.Wrapf(err, "failed to pull torrent infoHash=%v", in.GetInfoHash())
	}
	err = s.checkStoplist(torrent, hLog, t, in.GetInfoHash())
	if err != nil {
		return nil, err
	}
	// Guards consumers against malformed torrents stored before geometry
	// validation existed on Push.
	err = s.checkGeometry(torrent, hLog, t, in.GetInfoHash())
	if err != nil {
		return nil, err
	}
	if err = s.checkPayloadAbuseBytes(ctx, in.GetInfoHash(), torrent, hLog, t); err != nil {
		return nil, err
	}
	hLog.WithField("len", len(torrent)).WithField("duration", time.Since(t)).Info("sending torrent response")
	return &pb.PullReply{Torrent: []byte(torrent)}, nil
}

func (s *Server) checkGeometry(torrent []byte, log *log.Entry, t time.Time, hash string) error {
	mi, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		log.WithField("duration", time.Since(t)).WithError(err).Error("failed to parse stored torrent")
		return errors.Wrapf(err, "failed to parse stored torrent infoHash=%v", hash)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		log.WithField("duration", time.Since(t)).WithError(err).Error("failed to unmarshal stored info")
		return errors.Wrapf(err, "failed to unmarshal stored info infoHash=%v", hash)
	}
	if err := ValidateInfoGeometry(&info); err != nil {
		log.WithField("duration", time.Since(t)).WithError(err).Warn("malformed torrent")
		return status.Errorf(codes.FailedPrecondition, "malformed torrent infoHash=%v: %v", hash, err)
	}
	return nil
}

func (s *Server) checkStoplist(torrent []byte, log *log.Entry, t time.Time, hash string) error {
	if s.sl == nil {
		return nil
	}
	cr, err := s.sl.Check(torrent)
	if err != nil {
		log.WithField("duration", time.Since(t)).WithError(err).Error("failed to check stoplist")
		return errors.Wrapf(err, "failed to check stoplist infoHash=%v", hash)
	}
	if cr.Found {
		log.WithField("duration", time.Since(t)).Warnf("found in stoplist %v", cr.String())
		return status.Errorf(codes.PermissionDenied, "found in stoplist infoHash=%v: %s", hash, cr.String())
	}
	return nil
}

func (s *Server) Push(ctx context.Context, in *pb.PushRequest) (*pb.PushReply, error) {
	t := time.Now()
	reader := bytes.NewReader(in.GetTorrent())
	mi, err := metainfo.Load(reader)
	if err != nil {
		log.WithError(err).Error("failed to read torrent")
		return nil, err
	}
	infoHash := mi.HashInfoBytes().HexString()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "push")
	hLog.Info("push torrent request")

	err = s.checkStoplist(in.GetTorrent(), hLog, t, infoHash)
	if err != nil {
		return nil, err
	}

	abused, err := s.isAbused(ctx, infoHash)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", infoHash)
	}
	if abused {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", infoHash)
	}

	if err = s.checkPayloadAbuseBytes(ctx, infoHash, in.GetTorrent(), hLog, t); err != nil {
		return nil, err
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to unmarshal info")
		return nil, status.Errorf(codes.InvalidArgument, "failed to unmarshal info infoHash=%v: %v", infoHash, err)
	}
	if err := ValidateInfoGeometry(&info); err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("malformed torrent rejected")
		return nil, status.Errorf(codes.InvalidArgument, "malformed torrent infoHash=%v: %v", infoHash, err)
	}

	payload := in.GetTorrent()
	existing, err := s.s.pull(ctx, infoHash, 0)
	if err != nil && !errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("failed to read existing for merge; pushing as-is")
	} else if err == nil {
		merged, changed, mErr := mergeTorrent(existing, payload, s.defaultTrackers)
		if mErr != nil {
			hLog.WithField("duration", time.Since(t)).WithError(mErr).Warn("failed to merge; pushing incoming as-is")
		} else if !changed {
			hLog.WithField("len", len(payload)).WithField("duration", time.Since(t)).Info("torrent already present, no new announces — skipping push")
			return &pb.PushReply{InfoHash: infoHash}, nil
		} else {
			payload = merged
			// The payload differs from what pushm may have cached for this
			// infoHash minutes ago; without the drop the cached `true` would
			// swallow the write and the merged announces would be lost.
			s.s.pushm.Drop(infoHash)
			hLog.WithField("merged_len", len(merged)).Info("merged announces from existing torrent")
		}
	}

	_, err = s.s.Push(ctx, infoHash, payload)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to push")
		return nil, errors.Wrapf(err, "failed to push torrent infoHash=%v", infoHash)
	}
	s.s.pullm.Drop(infoHash)

	hLog.WithField("len", len(payload)).WithField("duration", time.Since(t)).Info("torrent succesfully pushed")
	return &pb.PushReply{InfoHash: infoHash}, nil
}

func (s *Server) Files(ctx context.Context, in *pb.FilesRequest) (*pb.FilesReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "files")
	hLog.Info("files manifest request")

	// Abuse is the hard legal gate (CSAM etc.) and is checked on every call,
	// including manifest cache hits, so a torrent banned after its manifest
	// was cached stops being listable immediately.
	abused, err := s.isAbused(ctx, infoHash)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", infoHash)
	}
	if abused {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", infoHash)
	}

	manifest, err := s.s.Manifest(ctx, infoHash, func(torrent []byte) ([]byte, error) {
		// Stoplist is enforced at build time, when we have the torrent bytes.
		if serr := s.checkStoplist(torrent, hLog, t, infoHash); serr != nil {
			return nil, serr
		}
		reply, berr := buildManifest(torrent)
		if berr != nil {
			return nil, berr
		}
		return proto.Marshal(reply)
	})
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return nil, status.Errorf(codes.NotFound, "unable to find torrent for infoHash=%v", infoHash)
	} else if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		// Preserve gRPC status set during build (e.g. stoplist PermissionDenied)
		// instead of flattening it into an Internal error.
		return nil, err
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to get manifest")
		return nil, errors.Wrapf(err, "failed to get manifest infoHash=%v", infoHash)
	}

	if err = s.checkPayloadAbuseCached(ctx, infoHash, hLog, t); err != nil {
		return nil, err
	}

	reply := &pb.FilesReply{}
	if err = proto.Unmarshal(manifest, reply); err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to unmarshal manifest")
		return nil, errors.Wrapf(err, "failed to unmarshal manifest infoHash=%v", infoHash)
	}
	hLog.WithField("files", len(reply.GetFiles())).WithField("duration", time.Since(t)).Info("sending files response")
	return reply, nil
}

// Fingerprint mirrors Files: abuse is the hard gate checked on every call
// including cache hits, the stoplist is enforced at build time when the
// torrent bytes are in hand, and the derived value is cached across tiers.
func (s *Server) Fingerprint(ctx context.Context, in *pb.FingerprintRequest) (*pb.FingerprintReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "fingerprint")
	hLog.Info("fingerprint request")

	abused, err := s.isAbused(ctx, infoHash)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", infoHash)
	}
	if abused {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", infoHash)
	}

	blob, err := s.s.Fingerprint(ctx, infoHash, func(torrent []byte) ([]byte, error) {
		if serr := s.checkStoplist(torrent, hLog, t, infoHash); serr != nil {
			return nil, serr
		}
		return buildFingerprint(torrent)
	})
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return nil, status.Errorf(codes.NotFound, "unable to find torrent for infoHash=%v", infoHash)
	} else if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		// Preserve the gRPC status set during build (e.g. stoplist
		// PermissionDenied) instead of flattening it into Internal.
		return nil, err
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to get fingerprint")
		return nil, errors.Wrapf(err, "failed to get fingerprint infoHash=%v", infoHash)
	}

	fps := parseFingerprint(blob)
	if len(fps) == 0 {
		hLog.WithField("duration", time.Since(t)).Error("cached fingerprint blob is unreadable")
		return nil, errors.Errorf("unreadable fingerprint infoHash=%v", infoHash)
	}
	reply := &pb.FingerprintReply{Value: fps[0].Value, Length: fps[0].Length}
	hLog.WithField("fingerprint", reply.GetValue()).WithField("duration", time.Since(t)).Info("sending fingerprint response")
	return reply, nil
}

// checkPayloadAbuse blocks a torrent whose PAYLOAD is blocked, even though its
// own infoHash has never been reported. Republishing one payload under a new
// name produces a new infoHash and would otherwise walk straight past the
// per-infoHash check.
//
// Runs after that check, not instead of it: the infoHash arm needs no torrent
// bytes, so a known-bad hash is refused without ever fetching anything.
//
// A failure here is logged and allowed through. The infoHash check is the hard
// legal gate and has already run; this arm is coverage of re-uploads, and
// taking the service down when the lookup is flaky would be the worse trade.
// checkPayloadAbuseCached is the read-path variant, used where the torrent
// bytes are NOT already in hand — Files answers from the cached manifest and
// never touches the .torrent.
//
// It therefore refuses to fetch one either. Deriving a fingerprint costs a
// full pull and parse, and putting that on a request that would otherwise be
// a single cache read regressed Files from an 11ms median to 28ms in
// production. When the fingerprint is not cached yet the derive is kicked off
// in the background and THIS request goes through unchecked on the payload
// arm — the infoHash arm, which is the hard legal gate, has already run, and
// the next request for the same torrent is covered.
func (s *Server) checkPayloadAbuseCached(ctx context.Context, h string, hLog *log.Entry, t time.Time) error {
	if s.a == nil {
		return nil
	}
	blob, err := s.s.CachedFingerprint(ctx, h)
	if err != nil {
		// Not derived yet, and deliberately not derived here. Measured in
		// production: 147 derives per 154 Files responses — the cache almost
		// never hits, because listings are overwhelmingly for torrents seen
		// once. Warming it in the background cost a pull and parse on nearly
		// every request while the check itself still could not run, so it was
		// all cost and no cover.
		//
		// The fingerprint is instead derived where the bytes are already in
		// hand: Push (ingest) and Pull (the .torrent that actually enables
		// downloading), both of which check unconditionally. Files returns a
		// listing, and it checks whenever a fingerprint happens to exist.
		return nil
	}
	return s.checkPayloadDigest(ctx, h, fingerprintDigest(blob), hLog, t)
}

// checkPayloadAbuseBytes is the Push-side variant: the torrent is in hand, so
// the fingerprint is derived directly instead of through the store. Refusing
// here keeps a payload that is already blocked from being stored at all,
// rather than storing it and refusing on every later read.
func (s *Server) checkPayloadAbuseBytes(ctx context.Context, h string, torrent []byte, hLog *log.Entry, t time.Time) error {
	if s.a == nil {
		return nil
	}
	blob, err := buildFingerprint(torrent)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("failed to derive fingerprint, skipping payload check")
		return nil
	}
	// Persist off the request path. This is the only place a fingerprint is
	// ever derived, so it is also what lets Files check at all — Files will
	// not fetch a .torrent of its own.
	s.s.CacheFingerprint(context.WithoutCancel(ctx), h, blob)
	return s.checkPayloadDigest(ctx, h, fingerprintDigest(blob), hLog, t)
}

func (s *Server) checkPayloadDigest(ctx context.Context, h string, digest []byte, hLog *log.Entry, t time.Time) error {
	if len(digest) == 0 {
		return nil
	}
	blocked, err := s.a.CheckFingerprint(ctx, digest)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("failed to check payload abuse, skipping")
		return nil
	}
	if blocked {
		hLog.WithField("duration", time.Since(t)).Warn("payload abused under another infoHash")
		return status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", h)
	}
	return nil
}

func (s *Server) isAbused(ctx context.Context, h string) (bool, error) {
	if s.a == nil {
		return false, nil
	}
	return s.a.Get(ctx, h)
}

func (s *Server) Touch(ctx context.Context, in *pb.TouchRequest) (*pb.TouchReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "touch")
	hLog.Info("touch torrent request")

	_, err := s.s.Touch(ctx, infoHash)
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return nil, status.Errorf(codes.NotFound, "torrent not found infoHash=%v", infoHash)
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to touch")
		return nil, errors.Wrapf(err, "failed to touch torrent infoHash=%v", infoHash)
	}

	hLog.WithField("duration", time.Since(t)).Info("sending touch reply")
	return &pb.TouchReply{}, nil
}
