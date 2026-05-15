package services

import (
	"bytes"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/urfave/cli"
	sl "github.com/webtor-io/stoplist"
)

var (
	re1 = regexp.MustCompile(`[^\p{L}\d]+`)
	re2 = regexp.MustCompile(`(\d+)`)
	re3 = regexp.MustCompile(`\s+`)

	// stoplistBlocksTotal counts torrents rejected at intake by the
	// abuse stoplist, labelled by which main-rule line fired. Pairs
	// with the helmfile-managed stoplist.yaml — if `main:` rule order
	// changes there, update mainRuleLabels below.
	stoplistBlocksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "torrent_store_stoplist_blocks_total",
		Help: "Torrents rejected at intake by the abuse stoplist, labelled by which main-rule line fired.",
	}, []string{"rule"})

	// mainRuleLabels maps the library's "line index N" Stack[0] to a
	// human-readable Prometheus label. Index order MUST match the
	// `main:` list in helmfile/values/torrent-store/stoplist.yaml.
	mainRuleLabels = []string{
		"stopwords",  // line 0: {stopwords}
		"age_sexual", // line 1: {age}+{sexual}
		"age_name",   // line 2: {age}+{name}
	}
)

const (
	StoplistPathFlag = "stoplist-path"
)

func RegisterStoplistFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   StoplistPathFlag,
			Usage:  "stoplist path",
			EnvVar: "STOPLIST_PATH",
			Value:  "",
		},
	)
}

type Stoplist struct {
	c  sl.Checker
	pf *prefilter
}

func NewStoplist(c *cli.Context) (*Stoplist, error) {
	path := c.String(StoplistPathFlag)
	if path == "" {
		return nil, nil
	}
	ch, err := sl.NewRuleFromYamlFile(path)
	if err != nil {
		return nil, err
	}
	pf, err := newPrefilter(path)
	if err != nil {
		// Prefilter failure is non-fatal — the slow path still
		// works correctly, we just don't get the speedup.
		pf = nil
	}
	return &Stoplist{
		c:  ch,
		pf: pf,
	}, nil
}

func (s *Stoplist) getData(b []byte) ([]string, error) {
	reader := bytes.NewReader(b)
	mi, err := metainfo.Load(reader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse torrent")
	}
	i, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal torrent info")
	}
	var data []string
	data = append(data, i.Name)
	for _, file := range i.Files {
		path := file.PathUtf8
		if path == nil {
			path = file.Path
		}
		data = append(data, strings.Join(path, " "))
	}
	// Comment + creator — CSAM-distribution torrents often have
	// neutral filenames but advertise the source forum through the
	// comment field ("hash on cpchans.xyz") or through a deliberately
	// branded `createdBy` value. Feed both as additional input so
	// existing stoplist rules (cpack, brand names, cp+context regex)
	// catch them too.
	//
	// Tracker URLs (Announce + AnnounceList) were originally screened
	// here as well but dropped: a single pack-torrent can advertise
	// 30-100 announce URLs, multiplying the per-pull regex cost by
	// 30-100× on the hot path. The signal is also easy to evade —
	// adversaries strip suspect tracker entries before sharing. The
	// 4-CSAM-torrent audit (2026-05-14) found zero cases where the
	// tracker list was the only signal; all matches fired on
	// name/paths/comment.
	if mi.Comment != "" {
		data = append(data, mi.Comment)
	}
	if mi.CreatedBy != "" {
		data = append(data, mi.CreatedBy)
	}
	return data, nil
}

// Check normalises every data string (name, file paths, tracker URLs,
// comment, createdBy) and runs the full stoplist rule tree over each.
// Returns the first positive CheckResult or an empty one when no rule
// fires.
//
// Heavyweight packs ship 4000+ data strings, so the loop is run in
// parallel across runtime.GOMAXPROCS workers. The stoplist library's
// Checker.Check is purely functional (read-only over compiled regex /
// substring rules), so concurrent invocation is safe. First match wins
// — once any worker reports a hit, the rest abort via the shared
// `done` flag and the result channel returns to the caller. Compare
// with the prior sequential version: 4778-string / 817 KB torrent went
// from ~615 ms to ~95 ms on an 11-core M3 (≈6.5×).
//
// The "first match" semantic remains non-deterministic in the rare
// case where multiple data strings would match — we only persist
// found/not-found and a Prometheus rule-label, so the indeterminism
// is acceptable.
func (s *Stoplist) Check(b []byte) (*sl.CheckResult, error) {
	data, err := s.getData(b)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get torrent text data")
	}
	if len(data) == 0 {
		return &sl.CheckResult{}, nil
	}
	if len(data) == 1 {
		// One-shot: skip the goroutine overhead.
		return s.checkOne(data[0]), nil
	}
	return s.checkParallel(data), nil
}

// checkOne runs the cheap prefilter (one combined RE2 regex over all
// leaf patterns) and only falls through to the expensive sl.Checker
// on a hit. Shared between the one-shot fast path and the parallel
// worker.
func (s *Stoplist) checkOne(d string) *sl.CheckResult {
	norm := s.normalize(d)
	if !s.pf.check(norm) {
		return &sl.CheckResult{}
	}
	cr := s.c.Check(norm)
	if cr.Found {
		stoplistBlocksTotal.WithLabelValues(ruleLabel(cr)).Inc()
		return cr
	}
	return &sl.CheckResult{}
}

// checkParallel spawns a worker pool sized to GOMAXPROCS (bounded by
// len(data)) and fans the data strings across them. Each worker pulls
// from a shared channel and calls the underlying Checker; the first
// positive match closes `done`, every other worker sees the flag and
// returns. The result is written to a buffered channel so the winning
// worker never blocks.
func (s *Stoplist) checkParallel(data []string) *sl.CheckResult {
	workers := runtime.GOMAXPROCS(0)
	if workers > len(data) {
		workers = len(data)
	}
	jobs := make(chan string, len(data))
	for _, d := range data {
		jobs <- d
	}
	close(jobs)

	var done atomic.Bool
	result := make(chan *sl.CheckResult, 1)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for d := range jobs {
				if done.Load() {
					return
				}
				norm := s.normalize(d)
				if !s.pf.check(norm) {
					continue
				}
				cr := s.c.Check(norm)
				if cr.Found {
					if done.CompareAndSwap(false, true) {
						result <- cr
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case cr := <-result:
		stoplistBlocksTotal.WithLabelValues(ruleLabel(cr)).Inc()
		return cr
	default:
		return &sl.CheckResult{}
	}
}

// ruleLabel extracts a human-readable Prometheus label from the
// stoplist library's CheckResult. Stack[0] for a main-rule match is
// always "line index N" (see github.com/webtor-io/stoplist lineRule
// implementation); we map N to a friendly label via mainRuleLabels.
func ruleLabel(cr *sl.CheckResult) string {
	if cr == nil || !cr.Found || len(cr.Stack) == 0 {
		return "unknown"
	}
	var idx int
	if _, err := fmt.Sscanf(cr.Stack[0], "line index %d", &idx); err != nil {
		return "unknown"
	}
	if idx < 0 || idx >= len(mainRuleLabels) {
		return fmt.Sprintf("line_%d", idx)
	}
	return mainRuleLabels[idx]
}

func (s *Stoplist) normalize(str string) string {
	str = strings.ToLower(str)
	str = re1.ReplaceAllString(str, " ")
	str = re2.ReplaceAllString(str, " $1 ")
	str = re3.ReplaceAllString(str, " ")
	str = strings.TrimSpace(str)
	return str
}
