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

type Server struct {
	pb.UnimplementedTorrentStoreServer
	s               *Store
	a               *Abuse
	sl              *Stoplist
	defaultTrackers []string
}

func NewServer(s *Store, a *Abuse, sl *Stoplist, defaultTrackers []string) *Server {
	return &Server{
		s:               s,
		a:               a,
		sl:              sl,
		defaultTrackers: defaultTrackers,
	}
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
	hLog.WithField("len", len(torrent)).WithField("duration", time.Since(t)).Info("sending torrent response")
	return &pb.PullReply{Torrent: []byte(torrent)}, nil
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

	reply := &pb.FilesReply{}
	if err = proto.Unmarshal(manifest, reply); err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to unmarshal manifest")
		return nil, errors.Wrapf(err, "failed to unmarshal manifest infoHash=%v", infoHash)
	}
	hLog.WithField("files", len(reply.GetFiles())).WithField("duration", time.Since(t)).Info("sending files response")
	return reply, nil
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
