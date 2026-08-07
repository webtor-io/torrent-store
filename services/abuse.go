package services

import (
	"context"
	"encoding/hex"
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
	*lazymap.LazyMap[bool]
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

// CheckFingerprint reports whether this content fingerprint belongs to blocked
// material — that is, whether this payload is blocked under some OTHER
// infohash we have never been told about.
//
// Cached by the fingerprint itself rather than by infoHash, so every re-upload
// of one payload shares a single cache entry and a single lookup.
func (s *Abuse) CheckFingerprint(ctx context.Context, fp []byte) (bool, error) {
	if len(fp) == 0 {
		return false, nil
	}
	return s.LazyMap.Get("fp:"+hex.EncodeToString(fp), func() (bool, error) {
		cl, err := s.cl.Get()
		if err != nil {
			return false, err
		}
		// Infohash deliberately empty: the caller has already checked it, and
		// leaving it out keeps this entry shared across every copy.
		r, err := cl.Check(ctx, &as.CheckRequest{Fingerprint: fp})
		if err != nil {
			return false, err
		}
		return r.GetExists(), nil
	})
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
