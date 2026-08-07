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
	// PushManifest stores a derived file manifest for the given infoHash.
	// A provider may no-op if it opts out of manifest caching.
	PushManifest(ctx context.Context, h string, manifest []byte) (ok bool, err error)
	// PullManifest returns a previously cached file manifest, or ErrNotFound.
	PullManifest(ctx context.Context, h string) (manifest []byte, err error)
	// PushFingerprint stores the derived content fingerprints of a torrent.
	// A provider may no-op if it opts out, exactly as for manifests.
	PushFingerprint(ctx context.Context, h string, fp []byte) (ok bool, err error)
	// PullFingerprint returns previously cached fingerprints, or ErrNotFound.
	PullFingerprint(ctx context.Context, h string) (fp []byte, err error)
	Name() string
}

type Store struct {
	pullm        *lazymap.LazyMap[[]byte]
	pushm        *lazymap.LazyMap[bool]
	touchm       *lazymap.LazyMap[bool]
	manifestm    *lazymap.LazyMap[[]byte]
	fingerprintm *lazymap.LazyMap[[]byte]
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
	manifestm := lazymap.New[[]byte](cfg)
	fingerprintm := lazymap.New[[]byte](cfg)
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
		manifestm:    &manifestm,
		fingerprintm: &fingerprintm,
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
	if a.Load() > 15 {
		return
	}
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

// pullManifest walks providers from `start`, returning the first cached
// manifest and backfilling the faster upper tiers on a hit. Mirrors pull,
// but for derived manifests; a provider that opts out of manifest caching
// reports ErrNotFound and is transparently skipped.
func (s *Store) pullManifest(ctx context.Context, h string, start int) (manifest []byte, err error) {
	for i := start; i < len(s.providers); i++ {
		t := time.Now()
		manifest, err = s.providers[i].PullManifest(ctx, h)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[i].Name()).Info("provider pull manifest")
		if manifest != nil {
			for j := 0; j < i; j++ {
				if _, perr := s.providers[j].PushManifest(ctx, h, manifest); perr != nil {
					log.WithField("infohash", h).WithField("provider", s.providers[j].Name()).WithError(perr).Warn("manifest not backfilled")
				}
			}
		}
		return
	}
	return nil, ErrNotFound
}

// pushManifest writes a manifest to every provider. Failures are non-fatal:
// a manifest is a rebuildable cache entry, so a partial write just means a
// future miss on the failed tier.
func (s *Store) pushManifest(ctx context.Context, h string, manifest []byte) {
	for _, v := range s.revProviders {
		t := time.Now()
		if _, err := v.PushManifest(ctx, h, manifest); err != nil {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).WithError(err).Warn("provider not pushed manifest")
			continue
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider push manifest")
	}
}

// Manifest returns the cached file manifest for h, building it via build()
// from the stored .torrent on a cache miss and persisting it across tiers.
// The whole get-or-build is singleflighted per infoHash so a cold burst on
// the same torrent triggers at most one Pull+parse. Manifests are immutable
// per infoHash, so no invalidation is needed.
func (s *Store) Manifest(ctx context.Context, h string, build func(torrent []byte) ([]byte, error)) ([]byte, error) {
	return s.manifestm.Get(h, func() ([]byte, error) {
		manifest, err := s.pullManifest(ctx, h, 0)
		if err == nil {
			return manifest, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		torrent, err := s.Pull(ctx, h)
		if err != nil {
			return nil, err
		}
		manifest, err = build(torrent)
		if err != nil {
			return nil, err
		}
		s.pushManifest(ctx, h, manifest)
		return manifest, nil
	})
}

// pullFingerprint walks providers from `start`, returning the first cached
// fingerprint blob and backfilling the faster upper tiers on a hit. Mirrors
// pullManifest.
func (s *Store) pullFingerprint(ctx context.Context, h string, start int) (fp []byte, err error) {
	for i := start; i < len(s.providers); i++ {
		t := time.Now()
		fp, err = s.providers[i].PullFingerprint(ctx, h)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[i].Name()).Info("provider pull fingerprint")
		if fp != nil {
			for j := 0; j < i; j++ {
				if _, perr := s.providers[j].PushFingerprint(ctx, h, fp); perr != nil {
					log.WithField("infohash", h).WithField("provider", s.providers[j].Name()).WithError(perr).Warn("fingerprint not backfilled")
				}
			}
		}
		return
	}
	return nil, ErrNotFound
}

// pushFingerprint writes a fingerprint blob to every provider. Failures are
// non-fatal: like a manifest it is rebuildable from the stored .torrent, so a
// partial write only costs a future miss on that tier.
func (s *Store) pushFingerprint(ctx context.Context, h string, fp []byte) {
	for _, v := range s.revProviders {
		t := time.Now()
		if _, err := v.PushFingerprint(ctx, h, fp); err != nil {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).WithError(err).Warn("provider not pushed fingerprint")
			continue
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider push fingerprint")
	}
}

// Fingerprint returns the cached content fingerprints for h, deriving them via
// build() from the stored .torrent on a miss and persisting them across tiers.
// Singleflighted per infoHash, and immutable per infoHash like a manifest, so
// no invalidation is needed.
//
// Worth caching even though the value is tiny: deriving it needs the whole
// .torrent, whose piece table dominates its size, so a cache hit avoids both
// pulling those bytes and parsing them.
func (s *Store) Fingerprint(ctx context.Context, h string, build func(torrent []byte) ([]byte, error)) ([]byte, error) {
	return s.fingerprintm.Get(h, func() ([]byte, error) {
		fp, err := s.pullFingerprint(ctx, h, 0)
		if err == nil {
			return fp, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		torrent, err := s.Pull(ctx, h)
		if err != nil {
			return nil, err
		}
		fp, err = build(torrent)
		if err != nil {
			return nil, err
		}
		// Persist off the request path. The S3 write alone costs ~50ms at the
		// median — two orders more than the Redis one — and the caller does
		// not need it to answer: the value is already in hand, the in-process
		// map holds it, and a lost write only costs a re-derive later, which
		// is what a failed write already costs.
		//
		// context.WithoutCancel so a client disconnecting mid-request does not
		// abort a write that is no longer on its behalf.
		go s.pushFingerprint(context.WithoutCancel(ctx), h, fp)
		return fp, nil
	})
}
