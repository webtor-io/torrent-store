package services

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/urfave/cli"
	as "github.com/webtor-io/abuse-store/proto"
	"github.com/webtor-io/lazymap"
)

const (
	AbuseUseFlag = "use-abuse"
)

func RegisterAbuseFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.BoolFlag{
			Name:   AbuseUseFlag,
			Usage:  "use abuse",
			EnvVar: "USE_ABUSE",
		},
	)
}

var (
	ErrAbuse = errors.New("store: torrent abused")
)

type Abuse struct {
	lazymap.LazyMap
	cl *AbuseClient
}

func NewAbuse(c *cli.Context, cl *AbuseClient) *Abuse {
	if !c.Bool(AbuseUseFlag) {
		return nil
	}
	return &Abuse{
		cl: cl,
		LazyMap: lazymap.New(&lazymap.Config{
			Expire:      time.Minute,
			ErrorExpire: 10 * time.Second,
		}),
	}
}

func (s *Abuse) get(h string) error {
	cl, err := s.cl.Get()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := cl.Check(ctx, &as.CheckRequest{Infohash: h})
	if err != nil {
		return err
	}
	if r.GetExists() {
		return ErrAbuse
	}
	return nil
}

func (s *Abuse) Get(h string) error {
	_, err := s.LazyMap.Get(h, func() (interface{}, error) {
		return nil, s.get(h)
	})
	if err != nil {
		return err
	}
	return nil
}
