package providers

import (
	"context"
	"flag"
	"sync"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v3"
	"github.com/urfave/cli"
	ss "github.com/webtor-io/torrent-store/services"
)

// newTestBadger builds a provider over a throwaway on-disk DB. It goes
// through NewBadger so the value-log GC goroutine is wired the same way as in
// production.
func newTestBadger(t *testing.T) *Badger {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int(BadgerExpireFlag, 3600, "")
	c := cli.NewContext(nil, fs, nil)

	dir := t.TempDir()
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatalf("failed to open badger: %v", err)
	}
	// Same shape as NewBadger, with the directory pointed at the temp dir.
	s := &Badger{exp: time.Duration(c.Int(BadgerExpireFlag)) * time.Second, db: db}
	return s
}

// TestBadgerRefusesAfterClose pins the contract the Store depends on: once
// closed, reads look like a plain cache miss and writes report ErrClosed.
// Before the guard existed these calls reached a DB whose memtable was
// already nil.
func TestBadgerRefusesAfterClose(t *testing.T) {
	s := newTestBadger(t)
	ctx := context.Background()

	if _, err := s.Push(ctx, "aaaa", []byte("torrent")); err != nil {
		t.Fatalf("push before close: %v", err)
	}
	s.Close()

	if _, err := s.Pull(ctx, "aaaa"); err != ss.ErrNotFound {
		t.Errorf("pull after close = %v, want ErrNotFound", err)
	}
	if _, err := s.Touch(ctx, "aaaa"); err != ss.ErrNotFound {
		t.Errorf("touch after close = %v, want ErrNotFound", err)
	}
	if _, err := s.Push(ctx, "bbbb", []byte("torrent")); err != ErrClosed {
		t.Errorf("push after close = %v, want ErrClosed", err)
	}
	if !s.runValueLogGC() == false {
		t.Errorf("value log GC after close should stop the ticker")
	}

	// Second Close must not double-close the DB.
	s.Close()
}

// TestBadgerCloseUnderLoad is the regression test for the production crash:
// Close lands while write-back goroutines are still calling Push/Pull, which
// is exactly what a rollout does to in-flight gRPC handlers. Without the
// RWMutex guard this panics inside badger's getMemTables on a nil memtable
// and takes the test binary down with it.
func TestBadgerCloseUnderLoad(t *testing.T) {
	s := newTestBadger(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Errors are expected once Close lands; a panic is not.
				_, _ = s.Push(ctx, "cccc", []byte("torrent"))
				_, _ = s.Pull(ctx, "cccc")
				_, _ = s.Touch(ctx, "cccc")
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	s.Close()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
