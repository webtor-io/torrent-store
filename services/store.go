package services

import (
	"time"

	"github.com/pkg/errors"
	"github.com/webtor-io/lazymap"

	log "github.com/sirupsen/logrus"
)

type StoreProvider interface {
	Push(h string, torrent []byte) (err error)
	Pull(h string) (torrent []byte, err error)
	Touch(h string) (err error)
	Name() string
}

type Store struct {
	pullm        *lazymap.LazyMap
	pushm        *lazymap.LazyMap
	touchm       *lazymap.LazyMap
	providers    []StoreProvider
	revProviders []StoreProvider
}

var (
	ErrNotFound = errors.New("store: torrent not found")
)

func NewStore(providers []StoreProvider) *Store {
	cfg := &lazymap.Config{
		ErrorExpire: 10 * time.Second,
		Expire:      time.Minute,
	}
	pullm := lazymap.New(cfg)
	pushm := lazymap.New(cfg)
	touchm := lazymap.New(cfg)
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
		providers:    providers,
		revProviders: revProviders,
	}
}

func (s *Store) push(h string, torrent []byte) (val interface{}, err error) {
	for _, v := range s.revProviders {
		t := time.Now()
		err = v.Push(h, torrent)
		if err != nil {
			return nil, err
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider push")
	}
	return
}

func (s *Store) touch(h string) (val interface{}, err error) {
	s.touchm.Touch(h)

	for i, v := range s.providers {
		t := time.Now()
		err = v.Touch(h)
		if err == ErrNotFound {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider not touched")
			continue
		} else if err != nil {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).WithError(err).Warn("provider has error")
			continue
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider touch")
		if i > 0 {
			go s.pull(h, i)
		}
		break
	}
	return
}

func (s *Store) pull(h string, start int) (torrent []byte, err error) {
	for i := start; i < len(s.providers); i++ {
		t := time.Now()
		torrent, err = s.providers[i].Pull(h)
		if err == ErrNotFound {
			continue
		} else if err != nil {
			return
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[i].Name()).Info("provider pull")
		if torrent != nil {
			for j := 0; j < i; j++ {
				log.WithField("infohash", h).WithField("provider", s.providers[j].Name()).Info("provider push")
				err = s.providers[j].Push(h, torrent)
				if err != nil {
					log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[j].Name()).WithError(err).Warn("provider not pushed")
					continue
				}
			}
		}
		break
	}
	return
}

func (s *Store) Pull(h string) (torrent []byte, err error) {
	v, err := s.pullm.Get(h, func() (interface{}, error) {
		return s.pull(h, 0)
	})
	if err != nil {
		return nil, err
	}
	torrent = v.([]byte)
	return
}

func (s *Store) Push(h string, torrent []byte) (err error) {
	_, err = s.pushm.Get(h, func() (interface{}, error) {
		return s.push(h, torrent)
	})
	return err
}

func (s *Store) Touch(h string) (err error) {
	_, err = s.touchm.Get(h, func() (interface{}, error) {
		return s.touch(h)
	})
	return err
}
