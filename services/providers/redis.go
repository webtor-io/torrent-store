package providers

import (
	"time"

	"github.com/go-redis/redis"
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

func (s *Redis) Touch(h string) (err error) {
	cl := s.cl.Get()

	res, err := cl.Expire(h, s.exp).Result()

	if err != nil {
		return err
	}
	if !res {
		return ss.ErrNotFound
	}
	return nil
}

func (s *Redis) Push(h string, torrent []byte) (err error) {
	cl := s.cl.Get()
	return cl.Set(h, torrent, s.exp).Err()
}

func (s *Redis) Pull(h string) (torrent []byte, err error) {
	cl := s.cl.Get()
	torrent, err = cl.Get(h).Bytes()
	if err == redis.Nil {
		return nil, ss.ErrNotFound
	}
	return
}
