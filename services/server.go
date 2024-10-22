package services

import (
	"bytes"
	"context"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedTorrentStoreServer
	s  *Store
	a  *Abuse
	sl *Stoplist
}

func NewServer(s *Store, a *Abuse, sl *Stoplist) *Server {
	return &Server{
		s:  s,
		a:  a,
		sl: sl,
	}
}

func (s *Server) Pull(ctx context.Context, in *pb.PullRequest) (*pb.PullReply, error) {
	t := time.Now()
	hLog := log.WithField("infoHash", in.GetInfoHash())
	hLog.Info("pull torrent request")

	err := s.isAbused(in.GetInfoHash())
	if errors.Is(err, ErrAbuse) {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "Restricted by the rightholder infoHash=%v", in.GetInfoHash())
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", in.GetInfoHash())
	}

	torrent, err := s.s.Pull(ctx, in.GetInfoHash())
	if errors.Is(err, ErrNotFound) {
		hLog.WithField("duration", time.Since(t)).Info("torrent not found")
		return nil, status.Errorf(codes.NotFound, "Unable to find torrent for infoHash=%v", in.GetInfoHash())
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
		return status.Errorf(codes.PermissionDenied, "Found in stoplist infoHash=%v", hash)
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
	hLog := log.WithField("infoHash", infoHash)
	hLog.Info("push torrent request")

	err = s.checkStoplist(in.GetTorrent(), hLog, t, infoHash)
	if err != nil {
		return nil, err
	}

	err = s.isAbused(infoHash)
	if errors.Is(err, ErrAbuse) {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return nil, status.Errorf(codes.PermissionDenied, "Restricted by the rightholder infoHash=%v", infoHash)
	} else if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return nil, errors.Wrapf(err, "failed to check abuse infoHash=%v", infoHash)
	}
	err = s.s.Push(ctx, infoHash, in.GetTorrent())
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to push")
		return nil, errors.Wrapf(err, "failed to push torrent infoHash=%v", infoHash)
	}

	hLog.WithField("len", len(in.GetTorrent())).WithField("duration", time.Since(t)).Info("torrent succesfully pushed")
	return &pb.PushReply{InfoHash: infoHash}, nil
}

func (s *Server) isAbused(h string) error {
	if s.a == nil {
		return nil
	}
	return s.a.Get(h)
}

func (s *Server) Touch(ctx context.Context, in *pb.TouchRequest) (*pb.TouchReply, error) {
	t := time.Now()
	infoHash := in.GetInfoHash()
	hLog := log.WithField("infoHash", infoHash)
	hLog.Info("touch torrent request")

	err := s.s.Touch(ctx, infoHash)
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
