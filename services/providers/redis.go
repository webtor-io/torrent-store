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

func (s *Redis) Touch(ctx context.Context, h string) (ok bool, err error) {
	cl := s.cl.Get()

	res, err := cl.Expire(ctx, h, s.exp).Result()

	if err != nil {
		return false, err
	}
	if !res {
		return false, ss.ErrNotFound
	}
	return true, nil
}

func (s *Redis) Push(ctx context.Context, h string, torrent []byte) (ok bool, err error) {
	cl := s.cl.Get()
	err = cl.Set(ctx, h, torrent, s.exp).Err()
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Redis) Pull(ctx context.Context, h string) (torrent []byte, err error) {
	cl := s.cl.Get()
	torrent, err = cl.Get(ctx, h).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ss.ErrNotFound
	}
	return
}

// derivedKey namespaces derived blobs so they never collide with the raw
// .torrent stored under the bare infoHash. The prefixes are FROZEN: entries
// written under them are still being read.
func derivedKey(kind ss.DerivedKind, h string) (string, error) {
	switch kind {
	case ss.DerivedManifest:
		return "m:" + h, nil
	case ss.DerivedFingerprint:
		return "fp:" + h, nil
	}
	return "", errors.Errorf("no redis key mapping for derived kind %q", kind)
}

func (s *Redis) PushDerived(ctx context.Context, kind ss.DerivedKind, h string, blob []byte) (ok bool, err error) {
	key, err := derivedKey(kind, h)
	if err != nil {
		return false, err
	}
	if err = s.cl.Get().Set(ctx, key, blob, s.exp).Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Redis) PullDerived(ctx context.Context, kind ss.DerivedKind, h string) (blob []byte, err error) {
	key, err := derivedKey(kind, h)
	if err != nil {
		return nil, err
	}
	blob, err = s.cl.Get().Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ss.ErrNotFound
	}
	return
}

var _ ss.StoreProvider = (*Redis)(nil)
