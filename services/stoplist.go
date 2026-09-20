package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	sl "github.com/webtor-io/stoplist"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// maxCommentRunes caps how much of the Comment field is fed into the
// stoplist. CSAM-distribution Comments are short forum-reference
// strings ("hash on cpchans.xyz", "tg.me/cp_channel" — typically
// well under 100 chars); audiobook/ebook torrents pack full
// multi-thousand-character book descriptions in here. Audit
// 2026-05-24 — Gabrielle Zevin "Tomorrow and Tomorrow and Tomorrow"
// audiobook (~2 KB description) was false-blocked by the
// `{age}+{sexual}` composite (`junior year at harvard` at pos 302
// + `lovers come together` at pos 92 in narrative prose).
// Truncating Comment to the first 300 runes keeps every CSAM-Comment
// pattern observed in production (all under 100 chars) while pushing
// the second co-occurring age-or-sexual token out of range — long
// English prose has high probability of containing some age+sexual
// whole-word pair within the first ~500 chars, but ~300 chars is
// tight enough to make the composite-FP very rare.
const maxCommentRunes = 300

// Rule-file layout (YAML, one list of lexemes per key):
//
//	<section>: [...]   vocabulary sections referenced from main as {section}
//	main:      [...]   ordered block rules; line 0 MUST be the direct-block
//	                   "{stopwords}" line, the rest are composites
//	except:    [...]   optional; /regex/ lexemes only, handled by this
//	                   service, never passed to the rule library
//
// `except` is a negative-context allowlist for the direct-block line
// only. RE2 has no lookahead, so a stopword sitting next to a track
// number, an episode number or a codec tag cannot be carved out inside
// the rule itself; instead, when line 0 fires and any except regex
// matches the SAME normalised string, that hit is discarded and the
// string is re-checked against the composite lines alone. Composite
// hits are never excepted.
const (
	stoplistMainKey   = "main"
	stoplistExceptKey = "except"

	// stoplistMainStopwordsLine is the main: line index the except
	// layer applies to. Mirrors mainRuleLabels[0].
	stoplistMainStopwordsLine = 0

	// exceptLogRunes caps the normalised string echoed in the
	// "excepted" log line.
	exceptLogRunes = 200
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

	// stoplistExceptedTotal counts stopwords hits discarded because an
	// `except:` regex matched the same normalised string. A composite
	// rule may still block the torrent afterwards — that block is
	// counted separately in stoplistBlocksTotal.
	stoplistExceptedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "torrent_store_stoplist_excepted_total",
		Help: "Stopwords hits discarded by an except: regex (composite rules may still block).",
	})

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

	// except holds the compiled `except:` regexes alongside their
	// source text (for the log line). Empty when the rule file has no
	// except section — then cc is nil and the except layer is inert.
	except []exceptRule
	// version identifies the rule file this Stoplist was built from (first
	// 16 hex of its sha256). Cached manifests carry it as a stamp, so a
	// stoplist change invalidates them on the next read.
	version string
	// cc is a second rule tree built from the same YAML with main:
	// reduced to its composite lines (line 0 dropped). Consulted only
	// after an except regex discarded a stopwords hit.
	cc sl.Checker
	// mainLines is len(main:) of the full tree; needed because the
	// library only prefixes "line index N" when a line rule has more
	// than one line.
	mainLines int
}

type exceptRule struct {
	src string
	re  *regexp.Regexp
}

func NewStoplist(c *cli.Context) (*Stoplist, error) {
	path := c.String(StoplistPathFlag)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read stoplist %q", path)
	}
	return newStoplistFromYaml(raw)
}

// newStoplistFromYaml parses the rule file, peels off the `except:`
// section (owned by this service, unknown to the rule library) and
// builds the full tree, the composite-only tree and the prefilter.
// A malformed except entry is a construction error, exactly like a
// malformed block rule — the service must not start on a rule file it
// cannot honour.
func newStoplistFromYaml(raw []byte) (*Stoplist, error) {
	sum := sha256.Sum256(raw)
	version := hex.EncodeToString(sum[:])[:manifestStampLen]
	sections := map[string][]string{}
	if err := yaml.Unmarshal(raw, sections); err != nil {
		return nil, errors.Wrap(err, "failed to parse stoplist yaml")
	}
	main, ok := sections[stoplistMainKey]
	if !ok {
		return nil, errors.Errorf("stoplist yaml has no %q section", stoplistMainKey)
	}
	except, err := compileExcept(sections[stoplistExceptKey])
	if err != nil {
		return nil, err
	}
	delete(sections, stoplistExceptKey)

	ch, err := sl.NewRule(sections)
	if err != nil {
		return nil, err
	}
	s := &Stoplist{
		version:   version,
		c:         ch,
		except:    except,
		mainLines: len(main),
	}
	if len(except) > 0 {
		composite := make(map[string][]string, len(sections))
		for k, v := range sections {
			composite[k] = v
		}
		composite[stoplistMainKey] = main[stoplistMainStopwordsLine+1:]
		if s.cc, err = sl.NewRule(composite); err != nil {
			return nil, errors.Wrap(err, "failed to build composite-only stoplist")
		}
	}
	pf, err := newPrefilterFromYaml(raw)
	if err != nil {
		// Prefilter failure is non-fatal — the slow path still
		// works correctly, we just don't get the speedup.
		pf = nil
	}
	s.pf = pf
	return s, nil
}

// compileExcept validates and compiles the except section. Entries
// must be `/regex/` lexemes: a bare word would silently compile to a
// substring match nobody asked for, so it is rejected instead.
func compileExcept(items []string) ([]exceptRule, error) {
	var out []exceptRule
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) < 2 || item[0] != '/' || item[len(item)-1] != '/' {
			return nil, errors.Errorf("stoplist %s entry %q is not a /regex/ lexeme", stoplistExceptKey, item)
		}
		re, err := regexp.Compile(item[1 : len(item)-1])
		if err != nil {
			return nil, errors.Wrapf(err, "failed to compile stoplist %s entry %q", stoplistExceptKey, item)
		}
		out = append(out, exceptRule{src: item, re: re})
	}
	return out, nil
}

func (s *Stoplist) getData(pt *parsedTorrent) []string {
	mi, i := pt.mi, &pt.info
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
		data = append(data, truncateRunes(mi.Comment, maxCommentRunes))
	}
	if mi.CreatedBy != "" {
		data = append(data, mi.CreatedBy)
	}
	return data
}

// truncateRunes returns the first `max` runes of s. Used to cap
// stoplist inputs that would otherwise drag long free-form text
// (book descriptions in Comment) through every rule and amplify
// substring-co-occurrence false positives.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

// Check parses raw torrent bytes and runs CheckParsed. Kept for callers
// without a parse in hand (standalone tools, tests); request paths parse once
// and use CheckParsed directly.
func (s *Stoplist) Check(b []byte) (*sl.CheckResult, error) {
	pt, err := parseTorrent(b)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get torrent text data")
	}
	return s.CheckParsed(pt)
}

// CheckParsed normalises every data string (name, file paths, comment,
// createdBy) and runs the full stoplist rule tree over each. Returns the
// first positive CheckResult or an empty one when no rule fires.
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
func (s *Stoplist) CheckParsed(pt *parsedTorrent) (*sl.CheckResult, error) {
	data := s.getData(pt)
	if len(data) == 0 {
		return &sl.CheckResult{}, nil
	}
	if len(data) == 1 {
		// One-shot: skip the goroutine overhead.
		return s.checkOne(data[0]), nil
	}
	return s.checkParallel(data), nil
}

// checkOne normalises one data string and runs checkNormalized over
// it, counting a block when it fires.
func (s *Stoplist) checkOne(d string) *sl.CheckResult {
	cr := s.checkNormalized(s.normalize(d))
	if cr.Found {
		stoplistBlocksTotal.WithLabelValues(ruleLabel(cr)).Inc()
	}
	return cr
}

// checkNormalized is THE decision for one normalised string: the cheap
// prefilter (one combined RE2 regex over all leaf patterns), the full
// sl.Checker only on a prefilter hit, then the except layer. Returns
// an empty result when nothing blocks. Shared by the one-shot path and
// the parallel worker so the two cannot drift.
func (s *Stoplist) checkNormalized(norm string) *sl.CheckResult {
	if !s.pf.check(norm) {
		return &sl.CheckResult{}
	}
	cr := s.c.Check(norm)
	if !cr.Found {
		return &sl.CheckResult{}
	}
	if len(s.except) == 0 || mainLineIndex(cr, s.mainLines) != stoplistMainStopwordsLine {
		return cr
	}
	ex := s.exceptMatch(norm)
	if ex == nil {
		return cr
	}
	stoplistExceptedTotal.Inc()
	log.WithFields(log.Fields{
		"pattern": ex.src,
		"data":    truncateRunes(norm, exceptLogRunes),
	}).Info("stoplist stopwords hit discarded by except rule")
	ccr := s.cc.Check(norm)
	if !ccr.Found {
		return &sl.CheckResult{}
	}
	return shiftMainLine(ccr, s.mainLines-1, stoplistMainStopwordsLine+1)
}

// exceptMatch returns the first except rule matching the normalised
// string, or nil.
func (s *Stoplist) exceptMatch(norm string) *exceptRule {
	for i := range s.except {
		if s.except[i].re.MatchString(norm) {
			return &s.except[i]
		}
	}
	return nil
}

// mainLineIndex returns which main: line produced cr. The library
// prefixes Stack[0] with "line index N" only when main has more than
// one line; a single-line main is line 0 by construction. -1 when the
// index cannot be determined.
func mainLineIndex(cr *sl.CheckResult, mainLines int) int {
	if cr == nil || !cr.Found {
		return -1
	}
	if mainLines == 1 {
		return 0
	}
	if len(cr.Stack) == 0 {
		return -1
	}
	var idx int
	if _, err := fmt.Sscanf(cr.Stack[0], "line index %d", &idx); err != nil {
		return -1
	}
	return idx
}

// shiftMainLine rewrites a hit from the composite-only tree (which has
// `lines` main lines) so its Stack[0] carries the line index of the
// FULL tree, i.e. the composite index plus `offset`. Keeps ruleLabel
// and the Prometheus label honest for excepted-then-composite blocks.
func shiftMainLine(cr *sl.CheckResult, lines, offset int) *sl.CheckResult {
	idx := mainLineIndex(cr, lines)
	if idx < 0 {
		return cr
	}
	label := fmt.Sprintf("line index %d", idx+offset)
	if lines == 1 {
		cr.Stack = append([]string{label}, cr.Stack...)
	} else {
		cr.Stack[0] = label
	}
	return cr
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
				cr := s.checkNormalized(s.normalize(d))
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
	idx := mainLineIndex(cr, len(mainRuleLabels))
	if idx < 0 {
		return "unknown"
	}
	if idx >= len(mainRuleLabels) {
		return fmt.Sprintf("line_%d", idx)
	}
	return mainRuleLabels[idx]
}

func (s *Stoplist) normalize(str string) string {
	// macOS-authored torrents carry NFD names; combining marks are not
	// \p{L}, so without composition re1 shreds "años" into "an os" and
	// every diacritic stoplist token misses.
	str = norm.NFC.String(str)
	str = strings.ToLower(str)
	str = re1.ReplaceAllString(str, " ")
	str = re2.ReplaceAllString(str, " $1 ")
	str = re3.ReplaceAllString(str, " ")
	str = strings.TrimSpace(str)
	return str
}

// Version identifies the rule file (16 hex of its sha256); "" when no
// stoplist is configured.
func (s *Stoplist) Version() string {
	if s == nil {
		return ""
	}
	return s.version
}
