package providers

import (
	"context"
	"github.com/pkg/errors"
	"github.com/redis/go-redis/v9"
	"time"

	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	ss "github.com/webtor-io/torrent-store/services"
)

const (
	RedisExpireFlag = "redis-expire"
	RedisUseFlag    = "use-redis"
)

func RegisterRedisFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.IntFlag{
			Name:   RedisExpireFlag,
			Usage:  "redis expire (sec)",
			Value:  3600 * 24,
			EnvVar: "REDIS_EXPIRE",
		},
		cli.BoolFlag{
			Name:   RedisUseFlag,
			Usage:  "use redis",
			EnvVar: "USE_REDIS",
		},
	)
}

type Redis struct {
	cl  *cs.RedisClient
	exp time.Duration
}

func NewRedis(c *cli.Context, cl *cs.RedisClient) *Redis {
	if !c.Bool(RedisUseFlag) {
		return nil
	}
	return &Redis{
		exp: time.Duration(c.Int(RedisExpireFlag)) * time.Second,
		cl:  cl,
	}
}

func (s *Redis) Name() string {
	return "redis"
}

func (s *Redis) Touch(ctx context.Context, h string) (err error) {
	cl := s.cl.Get()

	res, err := cl.Expire(ctx, h, s.exp).Result()

	if err != nil {
		return err
	}
	if !res {
		return ss.ErrNotFound
	}
	return nil
}

func (s *Redis) Push(ctx context.Context, h string, torrent []byte) (err error) {
	cl := s.cl.Get()
	return cl.Set(ctx, h, torrent, s.exp).Err()
}

func (s *Redis) Pull(ctx context.Context, h string) (torrent []byte, err error) {
	cl := s.cl.Get()
	torrent, err = cl.Get(ctx, h).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ss.ErrNotFound
	}
	return
}

var _ ss.StoreProvider = (*Redis)(nil)
