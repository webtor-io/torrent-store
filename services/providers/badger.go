package providers

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"

	badger "github.com/dgraph-io/badger/v3"
	"github.com/urfave/cli"
	ss "github.com/webtor-io/torrent-store/services"
)

const (
	BadgerExpireFlag = "badger-expire"
)

func RegisterBadgerFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.IntFlag{
			Name:   BadgerExpireFlag,
			Usage:  "badger expire (sec)",
			Value:  3600,
			EnvVar: "BADGER_EXPIRE",
		},
	)
}

// ErrClosed is returned by write operations issued after the provider has
// been closed. Reads report ss.ErrNotFound instead, so the Store just falls
// through to the next tier the way it does for any local-cache miss.
var ErrClosed = errors.New("badger provider is closed")

// Badger guards every DB operation with mu, held for read during an op and
// for write while closing.
//
// Badger's own IsClosed() check is not enough: on shutdown it sets db.mt to
// nil, and a Get/Set that passed the check a moment earlier then dereferences
// that nil inside getMemTables() and takes the whole process down with it.
// That is not hypothetical — three production crashes (2026-08-02 16:18,
// 2026-08-06 17:57 and 20:27 UTC) have this exact stack: our Push/Pull
// write-back -> badger txn.Commit -> db.get -> db.mt.IncrRef() on nil. All
// three landed during a rollout, because SIGTERM closes the DB while gRPC
// handlers are still in flight.
//
// The gRPC side now drains before providers close (see GRPCServer.Close), so
// in-flight requests should be finished by then. This lock is the second
// layer: anything still running — a lazymap write-back goroutine, the value
// log GC ticker — gets a clean error instead of a nil dereference.
type Badger struct {
	exp    time.Duration
	db     *badger.DB
	mu     sync.RWMutex
	closed bool
}

func NewBadger(c *cli.Context) (*Badger, error) {
	opt := badger.DefaultOptions("/tmp/badger")
	db, err := badger.Open(opt)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open badger db")
	}
	s := &Badger{
		exp: time.Duration(c.Int(BadgerExpireFlag)) * time.Second,
		db:  db,
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if !s.runValueLogGC() {
				return
			}
		}
	}()
	return s, nil
}

// runValueLogGC reports whether the ticker should keep running.
func (s *Badger) runValueLogGC() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false
	}
	return s.db.RunValueLogGC(0.7) == nil
}

func (s *Badger) Name() string {
	return "badger"
}

func (s *Badger) Touch(_ context.Context, h string) (ok bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, ss.ErrNotFound
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		i, err := txn.Get([]byte(h))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ss.ErrNotFound
		}
		// Any other Get error leaves `i` nil — must not dereference it.
		if err != nil {
			return err
		}
		return i.Value(func(val []byte) error {
			e := badger.NewEntry([]byte(h), val).WithTTL(s.exp)
			return txn.SetEntry(e)
		})
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Badger) Push(_ context.Context, h string, torrent []byte) (ok bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, ErrClosed
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry([]byte(h), torrent).WithTTL(s.exp)
		return txn.SetEntry(e)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Badger) Pull(_ context.Context, h string) (torrent []byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ss.ErrNotFound
	}
	err = s.db.View(func(txn *badger.Txn) (err error) {
		i, err := txn.Get([]byte(h))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ss.ErrNotFound
		}
		// Any other Get error leaves `i` nil — must not dereference it.
		if err != nil {
			return err
		}
		return i.Value(func(val []byte) error {
			torrent = val
			return nil
		})
	})
	return
}

// Badger intentionally opts out of manifest caching. The extra read/write
// volume from manifests on top of the torrent workload tripped a nil-pointer
// race inside Badger v3's memtable handling under load (a torrent-store pod
// crashed on 2026-06-14). Manifests are confined to the Redis (fast, shared)
// and S3 (durable) tiers instead, and rest-api fronts them with its own
// in-process cache, so dropping the local Badger L1 is barely noticeable.
func (s *Badger) PushManifest(_ context.Context, _ string, _ []byte) (ok bool, err error) {
	return true, nil
}

func (s *Badger) PullManifest(_ context.Context, _ string) (manifest []byte, err error) {
	return nil, ss.ErrNotFound
}

// Badger opts out of fingerprint caching for the same reason it opts out of
// manifests: extra read/write volume on top of the torrent workload is what
// tripped the Badger v3 memtable race. Fingerprints live in the Redis and S3
// tiers instead.
func (s *Badger) PushFingerprint(_ context.Context, _ string, _ []byte) (ok bool, err error) {
	return true, nil
}

func (s *Badger) PullFingerprint(_ context.Context, _ string) (fp []byte, err error) {
	return nil, ss.ErrNotFound
}

// Close blocks until every in-flight operation has left the DB, then closes
// it. Marking closed under the write lock is what makes the ordering safe:
// after Close returns, no goroutine can be inside s.db.
func (s *Badger) Close() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if already {
		return
	}
	_ = s.db.Close()
}

var _ ss.StoreProvider = (*Badger)(nil)
