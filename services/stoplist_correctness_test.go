package services

import (
	"os"
	"regexp"
	"strings"
	"testing"

	sl "github.com/webtor-io/stoplist"
)

// Reuses the normalize() pipeline from Stoplist verbatim to mirror
// what the production check sees.
var normRe1 = regexp.MustCompile(`[^\p{L}\d]+`)
var normRe2 = regexp.MustCompile(`(\d+)`)
var normRe3 = regexp.MustCompile(`\s+`)

func benchNormalize(s string) string {
	s = strings.ToLower(s)
	s = normRe1.ReplaceAllString(s, " ")
	s = normRe2.ReplaceAllString(s, " $1 ")
	s = normRe3.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// TestPrefilterCoverage runs every TP fixture from stoplist_eval
// through prefilter and verifies it returns true. A prefilter miss
// here would mean a CSAM input is silently allowed through Pull/Push
// once the gate is wired up — a correctness regression.
func TestPrefilterCoverage(t *testing.T) {
	yamlPath := os.Getenv("STOPLIST_BENCH_YAML")
	if yamlPath == "" {
		t.Skip("STOPLIST_BENCH_YAML not set")
	}
	pf, err := newPrefilter(yamlPath)
	if err != nil {
		t.Fatalf("prefilter compile: %v", err)
	}
	checker, err := sl.NewRuleFromYamlFile(yamlPath)
	if err != nil {
		t.Fatalf("checker: %v", err)
	}

	cases := []string{
		// CSAM TPs — must be caught
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		// 2026-05-14 audit additions
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		// 2026-05-18 audit additions
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
		"redacted",
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
