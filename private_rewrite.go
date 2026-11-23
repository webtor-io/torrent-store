package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	s "github.com/webtor-io/torrent-store/services"
	p "github.com/webtor-io/torrent-store/services/providers"
)

// makePrivateRewriteCMD builds the one-off that strips the first uploaders'
// trackers and webseeds from stored private torrents.
//
// A private Push stores info-only metainfo and keeps the uploader's trackers
// only in its sealed record, but copies stored before that still carry
// someone's passkey in announce, announce-list or url-list. This rewrites each
// one in place as infoOnly: the infohash and so the key are unchanged.
//
// Dry run unless --write. It lists the bucket root only, so private records
// under private/ are never listed or read and no PRIVATE_TRACKERS_KEY is
// needed. Redis copies are checked, and deleted with --write: the next Pull
// backfills the info-only copy from S3. Badger is per pod and out of reach;
// a write run is followed by a rollout restart (README).
//
// stdout gets one JSON object of counts at the end, also when interrupted;
// stderr gets progress lines and, per failure, the key and an error class.
// Neither ever carries a body, a URL or a host.
func makePrivateRewriteCMD() cli.Command {
	// No EnvVar on these: nothing in the pod's environment may turn on --write.
	cmd := cli.Command{
		Name:   "private-rewrite",
		Usage:  "strips trackers and webseeds from stored private torrents (dry run unless --write)",
		Action: privateRewrite,
		Flags: []cli.Flag{
			cli.BoolFlag{Name: "write", Usage: "rewrite the S3 objects and delete their Redis copies"},
			cli.BoolFlag{Name: "list-only", Usage: "only count the keys"},
			cli.IntFlag{Name: "concurrency", Value: 4, Usage: "objects examined at once"},
			cli.StringFlag{Name: "start-after", Usage: "resume after this key (last_key of an earlier run)"},
			cli.IntFlag{Name: "limit", Usage: "torrent objects to examine, 0 for all"},
			cli.IntFlag{Name: "progress-every", Value: 10000, Usage: "progress line on stderr every N torrent objects, 0 for none"},
		},
	}
	cmd.Flags = cs.RegisterS3ClientFlags(cmd.Flags)
	cmd.Flags = cs.RegisterRedisClientFlags(cmd.Flags)
	cmd.Flags = p.RegisterRedisFlags(cmd.Flags)
	cmd.Flags = p.RegisterS3Flags(cmd.Flags)
	return cmd
}

func privateRewrite(c *cli.Context) error {
	o := rewriteOptions{
		write: c.Bool("write"), listOnly: c.Bool("list-only"), concurrency: max(c.Int("concurrency"), 1),
		startAfter: c.String("start-after"), limit: c.Int("limit"), progressEvery: c.Int("progress-every"),
	}
	if o.write && o.listOnly {
		return errors.New("--write and --list-only exclude each other")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = o.concurrency
	// Badger is deliberately absent — see sharedProviders.
	shared, closeShared := sharedProviders(c, &http.Client{Timeout: time.Minute, Transport: tr})
	defer closeShared()
	w := &rewriter{o: o, log: os.Stderr}
	for _, sp := range shared {
		switch v := sp.(type) {
		case *p.S3:
			w.b = v
		case *p.Redis:
			w.c = v
		}
	}
	if w.b == nil {
		return errors.New("USE_S3 is not set: no bucket to rewrite")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := w.run(ctx)
	out, jerr := json.Marshal(w.r)
	if jerr != nil {
		return jerr
	}
	fmt.Println(string(out))
	if err != nil {
		return fmt.Errorf("private-rewrite stopped: %s", errClass(err))
	}
	return nil
}

// rewriteBucket is the part of *p.S3 the rewrite needs.
type rewriteBucket interface {
	ListRoot(ctx context.Context, startAfter string, page func(keys []string) bool) error
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, body []byte) error
	Versioning(ctx context.Context) (string, error)
}

// rewriteCache is the part of *p.Redis the rewrite needs.
type rewriteCache interface {
	Pull(ctx context.Context, h string) ([]byte, error)
	Delete(ctx context.Context, h string) error
}

type rewriteOptions struct {
	write, listOnly                   bool
	concurrency, limit, progressEvery int
	startAfter                        string
}

type rewriteReport struct {
	Mode       string `json:"mode"`
	Versioning string `json:"versioning"`
	Redis      bool   `json:"redis"`
	Torrents   int    `json:"torrents"`
	Other      int    `json:"other"`

	Public           int `json:"public"`
	PrivateInfoOnly  int `json:"private_info_only"`
	PrivateWithLists int `json:"private_with_lists"`
	HashMismatch     int `json:"hash_mismatch"`
	ParseErrors      int `json:"parse_errors"`
	GetErrors        int `json:"get_errors"`
	Rewritten        int `json:"rewritten"`
	WriteErrors      int `json:"write_errors"`

	RedisPresent   int `json:"redis_present"`
	RedisWithLists int `json:"redis_with_lists"`
	RedisDeleted   int `json:"redis_deleted"`
	RedisErrors    int `json:"redis_errors"`

	LastKey     string `json:"last_key"`
	Interrupted bool   `json:"interrupted"`
}

// hashKey is a stored .torrent: the bare infohash. <hash>.manifest,
// <hash>.fingerprint and anything else at the root count as other.
var hashKey = regexp.MustCompile(`^[0-9a-f]{40}$`)

type rewriter struct {
	b   rewriteBucket
	c   rewriteCache // nil without Redis
	o   rewriteOptions
	log io.Writer

	mu sync.Mutex // guards r and log
	r  rewriteReport
}

func (w *rewriter) count(n *int) {
	w.mu.Lock()
	*n++
	w.mu.Unlock()
}

// fail counts a failure and logs its key and class, never the error text: an
// S3 or bencode error can quote a URL or the body.
func (w *rewriter) fail(n *int, key, op, class string) {
	w.mu.Lock()
	*n++
	_, _ = fmt.Fprintf(w.log, "%s\t%s\t%s\n", key, op, class)
	w.mu.Unlock()
}

func errClass(err error) string {
	var ae awserr.Error
	switch {
	case errors.Is(err, s.ErrNotFound):
		return "not_found"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ae):
		return ae.Code()
	}
	return fmt.Sprintf("%T", err)
}

// run walks the root a listing page at a time and waits for a page before the
// next, so last_key is always a key everything up to which is done: resuming
// after it re-reads at most the page that was cut, and a re-read is harmless.
func (w *rewriter) run(ctx context.Context) error {
	w.r = rewriteReport{Mode: "dry-run", LastKey: w.o.startAfter, Redis: w.c != nil}
	switch {
	case w.o.write:
		w.r.Mode = "write"
	case w.o.listOnly:
		w.r.Mode = "list-only"
	}
	w.r.Versioning = "unknown"
	if v, err := w.b.Versioning(ctx); err != nil {
		_, _ = fmt.Fprintf(w.log, "-\tversioning\t%s\n", errClass(err))
	} else {
		w.r.Versioning = v
	}

	sem := make(chan struct{}, max(w.o.concurrency, 1))
	torrents, full := 0, false
	err := w.b.ListRoot(ctx, w.o.startAfter, func(keys []string) bool {
		var wg sync.WaitGroup
		last := ""
		for _, k := range keys {
			if ctx.Err() != nil {
				break
			}
			if !hashKey.MatchString(k) {
				w.count(&w.r.Other)
				last = k
				continue
			}
			if w.o.limit > 0 && torrents >= w.o.limit {
				full = true
				break
			}
			torrents++
			w.count(&w.r.Torrents)
			last = k
			if !w.o.listOnly {
				sem <- struct{}{}
				wg.Add(1)
				go func() {
					defer func() { <-sem; wg.Done() }()
					w.examine(ctx, k)
				}()
			}
			if w.o.progressEvery > 0 && torrents%w.o.progressEvery == 0 {
				w.mu.Lock()
				b, _ := json.Marshal(w.r)
				_, _ = fmt.Fprintf(w.log, "progress %s\n", b)
				w.mu.Unlock()
			}
		}
		wg.Wait()
		if ctx.Err() != nil {
			return false
		}
		if last != "" {
			w.mu.Lock()
			w.r.LastKey = last
			w.mu.Unlock()
		}
		return !full
	})
	if ctx.Err() != nil {
		w.r.Interrupted = true
		return ctx.Err()
	}
	return err
}

func (w *rewriter) examine(ctx context.Context, key string) {
	raw, err := w.b.GetObject(ctx, key)
	if err != nil {
		if ctx.Err() == nil {
			w.fail(&w.r.GetErrors, key, "get", errClass(err))
		}
		return
	}
	out, hash, private, err := s.StripPrivate(raw)
	switch {
	case err != nil:
		w.fail(&w.r.ParseErrors, key, "parse", "bencode")
		return
	case !private:
		w.count(&w.r.Public)
		return
	case out == nil:
		w.count(&w.r.PrivateInfoOnly)
	default:
		w.count(&w.r.PrivateWithLists)
		if hash != key {
			w.fail(&w.r.HashMismatch, key, "hash", "mismatch")
			return
		}
		if w.o.write {
			if err := w.b.PutObject(ctx, key, out); err != nil {
				w.fail(&w.r.WriteErrors, key, "put", errClass(err))
				return
			}
			w.count(&w.r.Rewritten)
		}
	}
	w.cache(ctx, key)
}

// cache checks the Redis copy of every private torrent, not only of those
// with lists in S3: a copy whose DEL failed on an earlier run sits over a
// clean S3 object, and the verification dry run has to see it. With --write
// it runs after the S3 write, so a Pull that then misses Redis backfills the
// info-only copy.
func (w *rewriter) cache(ctx context.Context, key string) {
	if w.c == nil {
		return
	}
	raw, err := w.c.Pull(ctx, key)
	if errors.Is(err, s.ErrNotFound) {
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			w.fail(&w.r.RedisErrors, key, "redis-get", errClass(err))
		}
		return
	}
	w.count(&w.r.RedisPresent)
	out, _, _, err := s.StripPrivate(raw)
	if err != nil {
		w.fail(&w.r.ParseErrors, key, "redis-parse", "bencode")
		return
	}
	if out == nil {
		return
	}
	w.count(&w.r.RedisWithLists)
	if !w.o.write {
		return
	}
	if err := w.c.Delete(ctx, key); err != nil {
		w.fail(&w.r.RedisErrors, key, "redis-del", errClass(err))
		return
	}
	w.count(&w.r.RedisDeleted)
}
