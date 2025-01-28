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
	lazymap.LazyMap[bool]
	cl *AbuseClient
}

func NewAbuse(c *cli.Context, cl *AbuseClient) *Abuse {
	if !c.Bool(AbuseUseFlag) {
		return nil
	}
	return &Abuse{
		cl: cl,
		LazyMap: lazymap.New[bool](&lazymap.Config{
			Expire:      time.Minute,
			StoreErrors: false,
		}),
	}
}

func (s *Abuse) Get(ctx context.Context, h string) (bool, error) {
	return s.LazyMap.Get(h, func() (bool, error) {
		cl, err := s.cl.Get()
		if err != nil {
			return false, err
		}
		r, err := cl.Check(ctx, &as.CheckRequest{Infohash: h})
		if err != nil {
			return false, err
		}
		if r.GetExists() {
			return true, nil
		}
		return false, nil
	})
}
