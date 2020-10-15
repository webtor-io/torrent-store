package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	joonix "github.com/joonix/log"
	log "github.com/sirupsen/logrus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	as "bitbucket.org/vintikzzzz/abuse-store/abuse-store"
	pb "github.com/webtor-io/torrent-store/torrent-store"

	"github.com/go-redis/redis"
	"github.com/urfave/cli"

	"github.com/anacrolix/torrent/metainfo"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"

	"github.com/pkg/errors"
)

type server struct {
	cl   redis.UniversalClient
	asCl as.AbuseStoreClient
}

func (s *server) isAbused(ctx context.Context, infoHash string) (bool, error) {
	if s.asCl == nil {
		return false, nil
	}
	inCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := s.asCl.Check(inCtx, &as.CheckRequest{Infohash: infoHash})
	if err != nil {
		return true, err
	}
	return r.Exists, nil
}

func (s *server) Check(ctx context.Context, in *pb.CheckRequest) (*pb.CheckReply, error) {
	log := log.WithField("infoHash", in.GetInfoHash())
	log.Info("Got check torrent request")
	res, err := s.cl.Exists(in.GetInfoHash()).Result()
	if err != nil {
		errors.Wrapf(err, "Failed to check torrent from redis infoHash=%v", in.GetInfoHash())
		return nil, err
	}
	exists := res == 1
	if exists {
		abused, err := s.isAbused(ctx, in.GetInfoHash())
		if err != nil {
			errors.Wrapf(err, "Failed to find out is content abused or not infoHash=%v", in.GetInfoHash())
			return nil, err
		}
		if abused {
			s.cl.Del(in.GetInfoHash())
			log.Warn("Content was abused")
			return &pb.CheckReply{Exists: false}, nil
		}
	}
	log.WithField("exists", exists).Info("Sending check response")
	return &pb.CheckReply{Exists: exists}, nil
}

func (s *server) Pull(ctx context.Context, in *pb.PullRequest) (*pb.PullReply, error) {
	log := log.WithField("infoHash", in.GetInfoHash())
	log.Info("Got pull torrent request")
	torrent, err := s.cl.Get(in.GetInfoHash()).Result()
	if err == redis.Nil {
		log.Info("Torrent not found")
		return nil, status.Errorf(codes.NotFound, "Unable to find torrent for infoHash=%v", in.GetInfoHash())
	}
	if err != nil {
		errors.Wrapf(err, "Failed to pull torrent from redis infoHash=%v", in.GetInfoHash())
		return nil, err
	}
	abused, err := s.isAbused(ctx, in.GetInfoHash())
	if err != nil {
		errors.Wrapf(err, "Failed to find out is content abused or not infoHash=%v", in.GetInfoHash())
		return nil, err
	}
	if abused {
		s.cl.Del(in.GetInfoHash())
		return nil, status.Errorf(codes.PermissionDenied, "Restricted by the rightholder infoHash=%v", in.GetInfoHash())
	}
	log.WithField("len", len(torrent)).Info("Sending torrent response")
	return &pb.PullReply{Torrent: []byte(torrent)}, nil
}

func (s *server) Push(ctx context.Context, in *pb.PushRequest) (*pb.PushReply, error) {
	reader := bytes.NewReader(in.GetTorrent())
	mi, err := metainfo.Load(reader)
	if err != nil {
		log.WithError(err).Error("Failed to read torrent")
		return nil, err
	}
	infoHash := mi.HashInfoBytes().HexString()
	log := log.WithField("infoHash", infoHash)
	abused, err := s.isAbused(ctx, infoHash)
	if err != nil {
		errors.Wrapf(err, "Failed to find out is content abused or not infoHash=%v", infoHash)
		return nil, err
	}
	if abused {
		return nil, status.Errorf(codes.PermissionDenied, "Restricted by the rightholder infoHash=%v", infoHash)
	}
	err = s.cl.Set(infoHash, in.GetTorrent(), time.Duration(in.Expire)*time.Second).Err()
	if err != nil {
		errors.Wrapf(err, "Failed to push torrent to redis infoHash=%v", infoHash)
	}
	log.WithField("len", len(in.Torrent)).Info("Torrent succesfully pushed")
	return &pb.PushReply{InfoHash: infoHash}, nil
}

func (s *server) Touch(ctx context.Context, in *pb.TouchRequest) (*pb.TouchReply, error) {
	infoHash := in.GetInfoHash()
	log := log.WithField("infoHash", infoHash)
	abused, err := s.isAbused(ctx, infoHash)
	if err != nil {
		errors.Wrapf(err, "Failed to find out is content abused or not infoHash=%v", infoHash)
		return nil, err
	}
	if abused {
		return nil, status.Errorf(codes.PermissionDenied, "Restricted by the rightholder infoHash=%v", infoHash)
	}
	res, err := s.cl.Expire(infoHash, time.Duration(in.Expire)*time.Second).Result()
	if err != nil {
		errors.Wrapf(err, "Failed to touch torrent infoHash=%v", infoHash)
	}
	if !res {
		return nil, status.Errorf(codes.NotFound, "Torrent not found infoHash=%v", infoHash)
	}
	log.Info("Sending touch reply")
	return &pb.TouchReply{}, nil
}

type serveOptions struct {
	redisPort          int
	redisHost          string
	redisPassword      string
	redisDB            int
	listeningPort      int
	listeningHost      string
	abuseStorePort     int
	abuseStoreHost     string
	sentinelPort       int
	sentinelMasterName string
}

func getRedisCLient(s *serveOptions) redis.UniversalClient {
	if s.sentinelPort != 0 {
		addrs := []string{fmt.Sprintf("%s:%d", s.redisHost, s.sentinelPort)}
		log.Infof("Using sentinel redis client with addrs=%v and master name=%v", addrs, s.sentinelMasterName)
		return redis.NewUniversalClient(&redis.UniversalOptions{
			Addrs:      addrs,
			Password:   "",
			DB:         0,
			MasterName: s.sentinelMasterName,
		})
	}
	addrs := []string{fmt.Sprintf("%s:%d", s.redisHost, s.redisPort)}
	log.Infof("Using default redis client with addrs=%v", addrs)
	return redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:    addrs,
		Password: "",
		DB:       0,
	})
}

func serve(opts *serveOptions) error {
	client := getRedisCLient(opts)
	defer client.Close()
	addr := fmt.Sprintf("%s:%d", opts.listeningHost, opts.listeningPort)
	lis, err := net.Listen("tcp", addr)
	defer lis.Close()
	if err != nil {
		return errors.Wrapf(err, "Failed to start listening tcp connections addr=%v", addr)
	}

	var asCl as.AbuseStoreClient
	if opts.abuseStoreHost != "" {
		asAddr := fmt.Sprintf("%s:%d", opts.abuseStoreHost, opts.abuseStorePort)
		asConn, err := grpc.Dial(asAddr, grpc.WithInsecure())
		defer asConn.Close()
		if err != nil {
			return errors.Wrapf(err, "Failed to dial abuse store addr=%v", asAddr)
		}
		asCl = as.NewAbuseStoreClient(asConn)
	}
	grpcError := make(chan error, 1)

	go func() {
		log.WithField("addr", addr).Info("Start listening for incoming GRPC connections")
		grpcLog := log.WithFields(log.Fields{})
		alwaysLoggingDeciderServer := func(ctx context.Context, fullMethodName string, servingObject interface{}) bool { return true }
		s := grpc.NewServer(
			grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
				grpc_ctxtags.StreamServerInterceptor(),
				grpc_logrus.StreamServerInterceptor(grpcLog),
				grpc_logrus.PayloadStreamServerInterceptor(grpcLog, alwaysLoggingDeciderServer),
				grpc_recovery.StreamServerInterceptor(),
			)),
			grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
				grpc_ctxtags.UnaryServerInterceptor(),
				grpc_logrus.UnaryServerInterceptor(grpcLog),
				grpc_logrus.PayloadUnaryServerInterceptor(grpcLog, alwaysLoggingDeciderServer),
				grpc_recovery.UnaryServerInterceptor(),
			)),
		)

		pb.RegisterTorrentStoreServer(s, &server{cl: client, asCl: asCl})

		reflection.Register(s)
		if err := s.Serve(lis); err != nil {
			grpcError <- err
		}
	}()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigs:
		log.WithField("signal", sig).Info("Got syscall")
	case err := <-grpcError:
		return errors.Wrapf(err, "Got GRPC error")
	}
	log.Info("Shooting down... at last!")
	return nil
}

func main() {
	log.SetFormatter(joonix.NewFormatter())
	app := cli.NewApp()
	app.Name = "torrent-store-server"
	app.Usage = "runs torrent store"
	app.Version = "0.0.1"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "redis-host, rH",
			Usage:  "hostname of the redis service",
			EnvVar: "REDIS_MASTER_SERVICE_HOST, REDIS_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   "redis-port, rP",
			Usage:  "port of the redis service",
			Value:  6379,
			EnvVar: "REDIS_MASTER_SERVICE_PORT, REDIS_SERVICE_PORT",
		},
		cli.StringFlag{
			Name:   "abuse-store-host, asH",
			Usage:  "hostname of the abuse store",
			EnvVar: "ABUSE_STORE_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   "abuse-store-port, asP",
			Usage:  "port of the redis service",
			Value:  50051,
			EnvVar: "ABUSE_STORE_SERVICE_PORT",
		},
		cli.IntFlag{
			Name:   "redis-db, rDB",
			Usage:  "redis db",
			Value:  0,
			EnvVar: "REDIS_DB",
		},
		cli.StringFlag{
			Name:   "redis-password, rPASS",
			Usage:  "redis password",
			Value:  "",
			EnvVar: "REDIS_PASS, REDIS_PASSWORD",
		},
		cli.StringFlag{
			Name:  "host, H",
			Usage: "listening host",
			Value: "",
		},
		cli.IntFlag{
			Name:  "port, P",
			Usage: "listening port",
			Value: 50051,
		},
		cli.IntFlag{
			Name:   "redis-sentinel-port",
			Usage:  "redis sentinel port",
			EnvVar: "REDIS_SERVICE_PORT_REDIS_SENTINEL",
		},
		cli.StringFlag{
			Name:   "redis-sentinel-master-name",
			Usage:  "redis sentinel master name",
			Value:  "mymaster",
			EnvVar: "REDIS_SERVICE_SENTINEL_MASTER_NAME",
		},
	}
	app.Action = func(c *cli.Context) error {
		if c.String("redis-host") == "" {
			return errors.New("No redis host defined")
		}
		return serve(&serveOptions{
			redisPort:          c.Int("redis-port"),
			redisHost:          c.String("redis-host"),
			redisDB:            c.Int("redis-db"),
			redisPassword:      c.String("redis-password"),
			listeningPort:      c.Int("port"),
			listeningHost:      c.String("host"),
			abuseStoreHost:     c.String("abuse-store-host"),
			abuseStorePort:     c.Int("abuse-store-port"),
			sentinelPort:       c.Int("redis-sentinel-port"),
			sentinelMasterName: c.String("redis-sentinel-master-name"),
		})
	}
	err := app.Run(os.Args)
	if err != nil {
		log.WithError(err).Fatal("Failed to serve application")
	}
}
