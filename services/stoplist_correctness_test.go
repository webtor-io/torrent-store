package services

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"

	sl "github.com/webtor-io/stoplist"
	"golang.org/x/text/unicode/norm"
)

// Reuses the normalize() pipeline from Stoplist verbatim to mirror
// what the production check sees.
var normRe1 = regexp.MustCompile(`[^\p{L}\d]+`)
var normRe2 = regexp.MustCompile(`(\d+)`)
var normRe3 = regexp.MustCompile(`\s+`)

func benchNormalize(s string) string {
	s = norm.NFC.String(s)
	s = strings.ToLower(s)
	s = normRe1.ReplaceAllString(s, " ")
	s = normRe2.ReplaceAllString(s, " $1 ")
	s = normRe3.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// TestPrefilterCoverage verifies the prefilter is a superset of the
// checker: every must-block case that fires in the full rule tree must
// also survive the fast prefilter, otherwise a CSAM input is silently
// allowed through Pull/Push once the gate is wired up.
//
// The must-block cases are real CSAM titles / evasion patterns — a map
// of what the filter catches, which is exactly what an attacker wants
// and must not live in this PUBLIC repo. They are loaded from an
// out-of-band corpus file instead (STOPLIST_CORPUS_BLOCK); the test
// skips when it (or STOPLIST_BENCH_YAML) is absent. The canonical
// corpus + a standalone runner live in the private infra/helmfile repo
// under values/torrent-store/stoplist-tests/ (see its README).
func TestPrefilterCoverage(t *testing.T) {
	yamlPath := os.Getenv("STOPLIST_BENCH_YAML")
	if yamlPath == "" {
		t.Skip("STOPLIST_BENCH_YAML not set")
	}
	corpusPath := os.Getenv("STOPLIST_CORPUS_BLOCK")
	if corpusPath == "" {
		t.Skip("STOPLIST_CORPUS_BLOCK not set (private must-block corpus)")
	}
	cases, err := readCorpus(corpusPath)
	if err != nil {
		t.Fatalf("corpus %q: %v", corpusPath, err)
	}
	if len(cases) == 0 {
		t.Fatalf("corpus %q is empty", corpusPath)
	}
	pf, err := newPrefilter(yamlPath)
	if err != nil {
		t.Fatalf("prefilter compile: %v", err)
	}
	checker, err := sl.NewRuleFromYamlFile(yamlPath)
	if err != nil {
		t.Fatalf("checker: %v", err)
	}

	for _, raw := range cases {
		norm := benchNormalize(raw)
		// Verify lib actually fires (sanity check on the case)
		cr := checker.Check(norm)
		if !cr.Found {
			t.Errorf("[lib miss] case did not fire in library: %q -> %q", raw, norm)
			continue
		}
		// Now verify prefilter says yes
		if !pf.check(norm) {
			t.Errorf("[PREFILTER MISS] %q -> %q would be silently allowed!", raw, norm)
		}
	}
}

// readCorpus reads one raw case per line from the out-of-band corpus
// file, skipping blank lines and `#` comments. The file is never part
// of this repo — see TestPrefilterCoverage.
func readCorpus(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}
