package services

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/webtor-io/lazymap"

	log "github.com/sirupsen/logrus"
)

type StoreProvider interface {
	Push(ctx context.Context, h string, torrent []byte) (ok bool, err error)
	Pull(ctx context.Context, h string) (torrent []byte, err error)
	Touch(ctx context.Context, h string) (ok bool, err error)
	Name() string
}

type Store struct {
	pullm        *lazymap.LazyMap[[]byte]
	pushm        *lazymap.LazyMap[bool]
	touchm       *lazymap.LazyMap[bool]
	providers    []StoreProvider
	revProviders []StoreProvider
	ratem        *lazymap.LazyMap[*atomic.Int64]
}

var (
	ErrNotFound = errors.New("store: torrent not found")
)

func NewStore(providers []StoreProvider) *Store {
	cfg := &lazymap.Config{
		Expire:      5 * time.Minute,
		StoreErrors: false,
	}

	rateCfg := &lazymap.Config{
		Expire:      1 * time.Minute,
		StoreErrors: false,
	}
	pullm := lazymap.New[[]byte](cfg)
	pushm := lazymap.New[bool](cfg)
	touchm := lazymap.New[bool](cfg)
	ratem := lazymap.New[*atomic.Int64](rateCfg)
	var revProviders []StoreProvider
	for _, p := range providers {
		log.WithField("provider", p.Name()).Info("use provider")

	}
	for i := len(providers) - 1; i >= 0; i-- {
		revProviders = append(revProviders, providers[i])
	}
	return &Store{
		pullm:        &pullm,
		pushm:        &pushm,
		touchm:       &touchm,
		ratem:        &ratem,
		providers:    providers,
		revProviders: revProviders,
	}
}

func (s *Store) push(ctx context.Context, h string, torrent []byte) (ok bool, err error) {
	for _, v := range s.revProviders {
		t := time.Now()
		ok, err = v.Push(ctx, h, torrent)
		if err != nil {
			return false, err
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider push")
	}
	return
}

func (s *Store) checkRate(h string) bool {
	a := s.getRate(h)
	return a.Load() < 10
}

func (s *Store) incRate(h string) {
	a := s.getRate(h)
	go func() {
		<-time.After(time.Minute)
		a.Add(-1)
	}()
	a.Add(1)
}

func (s *Store) getRate(h string) *atomic.Int64 {
	a, _ := s.ratem.Get(h, func() (*atomic.Int64, error) {
		return &atomic.Int64{}, nil
	})
	return a
}

func (s *Store) touch(ctx context.Context, h string) (ok bool, err error) {
	if !s.checkRate(h) {
		s.incRate(h)
		log.WithField("infohash", h).Warn("get rate limit")
		return false, ErrNotFound
	}
	s.touchm.Touch(h)
	for i, v := range s.providers {
		t := time.Now()
		ok, err = v.Touch(ctx, h)
		if errors.Is(err, ErrNotFound) {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider not touched")
			continue
		} else if err != nil {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).WithError(err).Warn("provider has error")
			continue
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider touch")
		if i > 0 {
			go func() {
				_, _ = s.pull(ctx, h, i)
			}()
		}
		break
	}
	if err != nil && errors.Is(err, ErrNotFound) {
		s.incRate(h)
	}
	return
}

func (s *Store) pull(ctx context.Context, h string, start int) (torrent []byte, err error) {
	if !s.checkRate(h) {
		s.incRate(h)
		log.WithField("infohash", h).Warn("get rate limit")
		return nil, ErrNotFound
	}
	for i := start; i < len(s.providers); i++ {
		t := time.Now()
		torrent, err = s.providers[i].Pull(ctx, h)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[i].Name()).Info("provider pull")
		if torrent != nil {
			for j := 0; j < i; j++ {
				log.WithField("infohash", h).WithField("provider", s.providers[j].Name()).Info("provider push")
				_, err = s.providers[j].Push(ctx, h, torrent)
				if err != nil {
					log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[j].Name()).WithError(err).Warn("provider not pushed")
					continue
				}
			}
		}
		break
	}
	if err != nil && errors.Is(err, ErrNotFound) {
		s.incRate(h)
	}
	return
}

func (s *Store) Pull(ctx context.Context, h string) ([]byte, error) {
	return s.pullm.Get(h, func() ([]byte, error) {
		return s.pull(ctx, h, 0)
	})

}

func (s *Store) Push(ctx context.Context, h string, torrent []byte) (bool, error) {
	return s.pushm.Get(h, func() (bool, error) {
		return s.push(ctx, h, torrent)
	})
}

func (s *Store) Touch(ctx context.Context, h string) (bool, error) {
	return s.touchm.Get(h, func() (bool, error) {
		return s.touch(ctx, h)
	})
}
