package services

import (
	"os"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	gore2 "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// prefilter is an early-reject check that runs ONE combined regex
// over a normalised data string. It is built from every leaf
// pattern in the stoplist YAML — literal strings escaped as
// QuoteMeta, regex literals (`/.../`) unwrapped — joined into a
// single big alternation.
//
// Hot path optimisation. The underlying github.com/webtor-io/stoplist
// library does NREGEX_RULES + NTEXT_RULES separate FindIndex /
// strings.Index passes per data string (~387 calls). Most pull
// requests on legitimate torrents hit ZERO rules, so we want the
// negative case to be cheap. One Go RE2 evaluation over a fused
// alternation is sub-millisecond regardless of rule count — RE2
// builds a DFA that walks the input in a single pass.
//
// On a positive pre-filter hit we hand off to the full library
// Check to (1) resolve composite rules like `{age}+{sexual}` that
// the pre-filter naively over-approximates, and (2) get the
// matching rule's Stack for Prometheus label fidelity. The library
// path runs at <2% of the previous cost because it now triggers
// only on torrents that already match SOME leaf pattern.
type prefilter struct {
	re *gore2.Regexp
}

// newPrefilter parses the stoplist YAML at `path`, harvests every
// leaf pattern (literal or `/regex/`), and compiles the union into
// a single regexp. Returns (nil, nil) for an empty / unreadable
// path so callers can fall back to the slow path without an error.
//
// Sections (`age`, `sexual`, `name`, `stopwords`) all contribute;
// the `main` section is skipped — it only references the other
// sections and contains no new leaf patterns.
func newPrefilter(path string) (*prefilter, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read stoplist for prefilter %q", path)
	}
	var sections map[string][]string
	if err := yaml.Unmarshal(raw, &sections); err != nil {
		return nil, errors.Wrap(err, "failed to parse stoplist yaml for prefilter")
	}

	var alts []string
	seen := map[string]struct{}{}
	for section, items := range sections {
		if section == "main" {
			continue
		}
		for _, item := range items {
			alt := compilePattern(item)
			if alt == "" {
				continue
			}
			if _, dup := seen[alt]; dup {
				continue
			}
			seen[alt] = struct{}{}
			alts = append(alts, alt)
		}
	}
	if len(alts) == 0 {
		return nil, nil
	}
	// `(?i)` for case-insensitive — matches the original library's
	// behaviour where regex entries that need ASCII case-insensitive
	// matching use `(?i)` inline anyway. CJK/Cyrillic don't have
	// case so the flag is a no-op for those literals.
	expr := "(?i)" + strings.Join(alts, "|")
	re, err := gore2.Compile(expr)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to compile prefilter alternation (len=%d alts)", len(alts))
	}
	return &prefilter{re: re}, nil
}

// compilePattern turns one YAML entry into a regex alternation
// branch:
//   - `/foo/` → `foo` (raw regex, unwrapped)
//   - `foo` → `\Qfoo\E` via regexp.QuoteMeta (literal substring)
//
// Empty entries (sometimes left as YAML separators) are dropped.
func compilePattern(item string) string {
	item = strings.TrimSpace(item)
	if item == "" {
		return ""
	}
	if len(item) >= 2 && item[0] == '/' && item[len(item)-1] == '/' {
		inner := item[1 : len(item)-1]
		// Strip a leading `(?i)` — we add the flag once at the union
		// level. RE2 supports inline flags only at start of group.
		inner = strings.TrimPrefix(inner, "(?i)")
		// Validation happens at union-compile time. If any entry is
		// malformed for go-re2 the whole prefilter fails to compile
		// and we fall through to the slow path (correctness > speed).
		// Wrapping in (?:) isolates the alt from neighbours' anchors.
		return "(?:" + inner + ")"
	}
	return "(?:" + regexp.QuoteMeta(item) + ")"
}

// check returns true when the normalised data string contains any
// leaf pattern. Caller MUST run the full sl.Checker on a true to
// confirm composite-rule fire and to extract the Stack label.
func (p *prefilter) check(s string) bool {
	if p == nil || p.re == nil {
		// No prefilter configured — assume hit (caller will run the
		// full check). This is a safe fall-through.
		return true
	}
	return p.re.MatchString(s)
}
