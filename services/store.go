package services

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/webtor-io/lazymap"

	log "github.com/sirupsen/logrus"
)

// DerivedKind names a derived, immutable, rebuildable blob cached alongside
// the raw .torrent: the file manifest, the content fingerprint. All kinds
// share one contract — keyed by infoHash, rebuildable from the stored
// .torrent, safe to lose from any tier — so providers and the Store handle
// them through one code path. A provider maps a kind onto its own storage
// key; those key formats are FROZEN, because blobs written under them (S3
// carries no expiry) are still being read.
type DerivedKind string

const (
	DerivedManifest    DerivedKind = "manifest"
	DerivedFingerprint DerivedKind = "fingerprint"
)

type StoreProvider interface {
	Push(ctx context.Context, h string, torrent []byte) (ok bool, err error)
	Pull(ctx context.Context, h string) (torrent []byte, err error)
	Touch(ctx context.Context, h string) (ok bool, err error)
	// PushDerived stores a derived blob of the given kind for the infoHash.
	// A provider may no-op if it opts out of derived caching.
	PushDerived(ctx context.Context, kind DerivedKind, h string, blob []byte) (ok bool, err error)
	// PullDerived returns a previously cached derived blob, or ErrNotFound —
	// also when the provider opts out of derived caching entirely.
	PullDerived(ctx context.Context, kind DerivedKind, h string) (blob []byte, err error)
	Name() string
}

type Store struct {
	pullm        *lazymap.LazyMap[[]byte]
	pushm        *lazymap.LazyMap[bool]
	touchm       *lazymap.LazyMap[bool]
	derivedm     *lazymap.LazyMap[[]byte]
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
	derivedm := lazymap.New[[]byte](cfg)
	ratem := lazymap.New[*atomic.Int64](rateCfg)
	var revProviders []StoreProvider
	for _, p := range providers {
		log.WithField("provider", p.Name()).Info("use provider")
	}
	for i := len(providers) - 1; i >= 0; i-- {
		revProviders = append(revProviders, providers[i])
	}
	return &Store{
		pullm:        pullm,
		pushm:        pushm,
		touchm:       touchm,
		derivedm:     derivedm,
		ratem:        ratem,
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

// incRate counts misses. No decrement is needed: the rate entry expires 60s
// after creation (ratem's Expire, and nothing ever refreshes it), and that
// eviction is what actually resets the window. The per-increment decrement
// goroutines this used to spawn always fired at or after the eviction, onto
// an orphaned counter.
func (s *Store) incRate(h string) {
	s.getRate(h).Add(1)
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
			// Warm the upper tiers off the request path. WithoutCancel:
			// the RPC returning (which it does immediately) cancels the
			// request context, and a warm-up killed on line one warms
			// nothing.
			warmCtx := context.WithoutCancel(ctx)
			go func() {
				_, _ = s.pull(warmCtx, h, i)
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
				// Backfill failure is local: the torrent is already in hand,
				// and an upper cache tier being down must not fail the pull.
				if _, perr := s.providers[j].Push(ctx, h, torrent); perr != nil {
					log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[j].Name()).WithError(perr).Warn("provider not pushed")
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

// pullDerived walks providers from `start`, returning the first cached blob
// of the kind and backfilling the faster upper tiers on a hit. Mirrors pull,
// but for derived blobs; a provider that opts out of derived caching reports
// ErrNotFound and is transparently skipped.
func (s *Store) pullDerived(ctx context.Context, kind DerivedKind, h string, start int) (blob []byte, err error) {
	for i := start; i < len(s.providers); i++ {
		t := time.Now()
		blob, err = s.providers[i].PullDerived(ctx, kind, h)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", s.providers[i].Name()).Info("provider pull " + string(kind))
		if blob != nil {
			for j := 0; j < i; j++ {
				if _, perr := s.providers[j].PushDerived(ctx, kind, h, blob); perr != nil {
					log.WithField("infohash", h).WithField("provider", s.providers[j].Name()).WithError(perr).Warn(string(kind) + " not backfilled")
				}
			}
		}
		return
	}
	return nil, ErrNotFound
}

// pushDerived writes a derived blob to every provider. Failures are
// non-fatal: the blob is rebuildable from the stored .torrent, so a partial
// write just means a future miss on the failed tier.
func (s *Store) pushDerived(ctx context.Context, kind DerivedKind, h string, blob []byte) {
	for _, v := range s.revProviders {
		t := time.Now()
		if _, err := v.PushDerived(ctx, kind, h, blob); err != nil {
			log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).WithError(err).Warn("provider not pushed " + string(kind))
			continue
		}
		log.WithField("infohash", h).WithField("duration", time.Since(t)).WithField("provider", v.Name()).Info("provider push " + string(kind))
	}
}

// getOrBuildDerived returns the cached blob of the kind, building it via
// build() from the stored .torrent on a miss and persisting it across tiers.
// The whole get-or-build is singleflighted per (kind, infoHash) so a cold
// burst on the same torrent triggers at most one Pull+parse. Derived blobs
// are immutable per infoHash, so no invalidation is needed.
//
// persistAsync moves the tier writes off the request path, with
// context.WithoutCancel so a client disconnecting mid-request does not abort
// a write that is no longer on its behalf. The caller does not need the write
// to answer — the value is already in hand and the in-process map holds it —
// and a lost write only costs a re-derive later, which is what a failed write
// already costs.
func (s *Store) getOrBuildDerived(ctx context.Context, kind DerivedKind, h string, build func(torrent []byte) ([]byte, error), persistAsync bool) ([]byte, error) {
	return s.derivedm.Get(string(kind)+":"+h, func() ([]byte, error) {
		blob, err := s.pullDerived(ctx, kind, h, 0)
		if err == nil {
			return blob, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		torrent, err := s.Pull(ctx, h)
		if err != nil {
			return nil, err
		}
		blob, err = build(torrent)
		if err != nil {
			return nil, err
		}
		if persistAsync {
			go s.pushDerived(context.WithoutCancel(ctx), kind, h, blob)
		} else {
			s.pushDerived(ctx, kind, h, blob)
		}
		return blob, nil
	})
}

// Manifest returns the cached file manifest for h. Persisted synchronously —
// moving it off the request path is a measurable behaviour change for Files
// and gets decided on its own numbers, not inherited from fingerprints.
func (s *Store) Manifest(ctx context.Context, h string, build func(torrent []byte) ([]byte, error)) ([]byte, error) {
	return s.getOrBuildDerived(ctx, DerivedManifest, h, build, false)
}

// Fingerprint returns the cached content fingerprints for h.
//
// Worth caching even though the value is tiny: deriving it needs the whole
// .torrent, whose piece table dominates its size, so a cache hit avoids both
// pulling those bytes and parsing them. Persisted asynchronously — the S3
// write alone costs ~50ms at the median, two orders more than the Redis one.
func (s *Store) Fingerprint(ctx context.Context, h string, build func(torrent []byte) ([]byte, error)) ([]byte, error) {
	return s.getOrBuildDerived(ctx, DerivedFingerprint, h, build, true)
}

// CachedFingerprint returns an already-derived fingerprint, or ErrNotFound.
// It reads the cache tiers only — it never pulls or parses a .torrent — so it
// is safe on a request that would otherwise be a single cache read.
func (s *Store) CachedFingerprint(ctx context.Context, h string) ([]byte, error) {
	return s.pullDerived(ctx, DerivedFingerprint, h, 0)
}

// CacheFingerprint makes sure an already-derived fingerprint is cached,
// without blocking the caller. Used where the bytes were free — the torrent
// was in hand anyway — so the value costs nothing to produce and everything
// downstream can read it.
//
// It probes the tiers before writing: Pull re-derives the fingerprint on
// every cold request, the cached object is immutable, and re-putting it into
// S3 each time is write amplification with nothing to show for it. The probe
// itself backfills faster tiers on a partial hit, so the only case that still
// writes is a full miss.
func (s *Store) CacheFingerprint(ctx context.Context, h string, fp []byte) {
	go func() {
		if _, err := s.pullDerived(ctx, DerivedFingerprint, h, 0); err == nil {
			return
		}
		s.pushDerived(ctx, DerivedFingerprint, h, fp)
	}()
}
