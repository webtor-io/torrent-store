package services

import (
	"context"
	"strings"
	"time"

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

	if err := s.gateInfoHash(ctx, in.GetInfoHash(), hLog, t); err != nil {
		return nil, err
	}
	torrent, err := s.s.Pull(ctx, in.GetInfoHash())
	if err != nil {
		return nil, rpcError(err, hLog, t, in.GetInfoHash(), "failed to pull torrent")
	}
	pt, err := parseTorrent(torrent)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to parse stored torrent")
		return nil, errors.Wrapf(err, "failed to parse stored torrent infoHash=%v", in.GetInfoHash())
	}
	err = s.checkStoplist(pt, hLog, t, in.GetInfoHash())
	if err != nil {
		return nil, err
	}
	// Guards consumers against malformed torrents stored before geometry
	// validation existed on Push.
	if err := ValidateInfoGeometry(&pt.info); err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("malformed torrent")
		return nil, status.Errorf(codes.FailedPrecondition, "malformed torrent infoHash=%v: %v", in.GetInfoHash(), err)
	}
	if err = s.gatePayload(ctx, in.GetInfoHash(), pt, hLog, t); err != nil {
		return nil, err
	}
	hLog.WithField("len", len(torrent)).WithField("duration", time.Since(t)).Info("sending torrent response")
	return &pb.PullReply{Torrent: []byte(torrent)}, nil
}

func (s *Server) checkStoplist(pt *parsedTorrent, log *log.Entry, t time.Time, hash string) error {
	if s.sl == nil {
		return nil
	}
	cr, err := s.sl.CheckParsed(pt)
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
	pt, err := parseTorrent(in.GetTorrent())
	if err != nil {
		log.WithError(err).Error("failed to read torrent")
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse torrent: %v", err)
	}
	infoHash := pt.mi.HashInfoBytes().HexString()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "push")
	hLog.Info("push torrent request")

	err = s.checkStoplist(pt, hLog, t, infoHash)
	if err != nil {
		return nil, err
	}

	if err = s.gateInfoHash(ctx, infoHash, hLog, t); err != nil {
		return nil, err
	}
	if err = s.gatePayload(ctx, infoHash, pt, hLog, t); err != nil {
		return nil, err
	}

	if err := ValidateInfoGeometry(&pt.info); err != nil {
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

	hLog.WithField("len", len(payload)).WithField("duration", time.Since(t)).Info("torrent successfully pushed")
	return &pb.PushReply{InfoHash: infoHash}, nil
}

func (s *Server) Files(ctx context.Context, in *pb.FilesRequest) (*pb.FilesReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "files")
	hLog.Info("files manifest request")

	// The gate runs on every call, including manifest cache hits, so a
	// torrent banned after its manifest was cached stops being listable
	// immediately.
	if err := s.gateInfoHash(ctx, infoHash, hLog, t); err != nil {
		return nil, err
	}

	manifest, err := s.s.Manifest(ctx, infoHash, func(torrent []byte) ([]byte, error) {
		pt, perr := parseTorrent(torrent)
		if perr != nil {
			return nil, perr
		}
		// Stoplist is enforced at build time, when we have the torrent bytes.
		if serr := s.checkStoplist(pt, hLog, t, infoHash); serr != nil {
			return nil, serr
		}
		reply, berr := buildManifestParsed(pt)
		if berr != nil {
			return nil, berr
		}
		return proto.Marshal(reply)
	})
	if err != nil {
		return nil, rpcError(err, hLog, t, infoHash, "failed to get manifest")
	}

	if err = s.gatePayload(ctx, infoHash, nil, hLog, t); err != nil {
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

// Fingerprint mirrors Files: the infoHash gate runs on every call including
// cache hits, the stoplist is enforced at build time when the torrent bytes
// are in hand, and the derived value is cached across tiers. The payload arm
// deliberately does not run here — see the matrix in abuse_gate.go.
func (s *Server) Fingerprint(ctx context.Context, in *pb.FingerprintRequest) (*pb.FingerprintReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "fingerprint")
	hLog.Info("fingerprint request")

	if err := s.gateInfoHash(ctx, infoHash, hLog, t); err != nil {
		return nil, err
	}

	blob, err := s.s.Fingerprint(ctx, infoHash, func(torrent []byte) ([]byte, error) {
		pt, perr := parseTorrent(torrent)
		if perr != nil {
			return nil, perr
		}
		if serr := s.checkStoplist(pt, hLog, t, infoHash); serr != nil {
			return nil, serr
		}
		return buildFingerprintParsed(pt)
	})
	if err != nil {
		return nil, rpcError(err, hLog, t, infoHash, "failed to get fingerprint")
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

// rpcError triages an internal error into the reply: ErrNotFound becomes
// codes.NotFound, an error already carrying a gRPC status (e.g. the
// stoplist's PermissionDenied surfacing from a build callback) is preserved
// rather than flattened into Internal, and anything else is logged and
// wrapped with msg.
func rpcError(err error, hLog *log.Entry, t time.Time, h string, msg string) error {
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return status.Errorf(codes.NotFound, "unable to find torrent for infoHash=%v", h)
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		return err
	}
	hLog.WithField("duration", time.Since(t)).WithError(err).Error(msg)
	return errors.Wrapf(err, "%s infoHash=%v", msg, h)
}

// Touch runs no abuse gate at all — see the matrix in abuse_gate.go: it only
// refreshes the storage TTL, and serving is gated at Pull/Files.
func (s *Server) Touch(ctx context.Context, in *pb.TouchRequest) (*pb.TouchReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash).WithField("method", "touch")
	hLog.Info("touch torrent request")

	_, err := s.s.Touch(ctx, infoHash)
	if err != nil {
		return nil, rpcError(err, hLog, t, infoHash, "failed to touch torrent")
	}

	hLog.WithField("duration", time.Since(t)).Info("sending touch reply")
	return &pb.TouchReply{}, nil
}
