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
		"loland full video pack fekla sonya melissa emma olga",
		"loland fekla suck and fuck mp 4",
		"full pack 95 loland",
		"2024 福建兄妹 暑假作业 我本初中 台湾姐妹 韩国n号房 我要出彩 紫色面具等 1 tb小萝莉幼初中生高中生资源购买联系客服邮件 xuexiclub gmail com",
		"白丝萝莉足交 1 mov",
		"full pack loland 2022 hq pthc pollyfan real incest cp preteen videos stars stripes julyjailbait",
		"candydoll tv video pack",
		// 2026-05-14 audit additions
		"raping little girl webcam skype omegle vids jbtcam xplay young mega links zoo orgasm zoofilia teen",
		"2 4tb videos 12 15 hot lolitte gril forbidden video studio lolita top nn omegle thread",
		"jb amateur teen squirts",
		"cp选",
		"art of zoo 04 hd mp4",
		"bestiality videos pack hd 720p",
		// 2026-05-18 audit additions
		"kingspass lolita 8 12 yrs 10 yrs",
		"nastia mouse video",
		"nastya mouse hd",
		"valya final version part 03",
		"valya sister final version part 1",
		"valya 12 custom",
		"valya 28",
		"001 chatroulette omegle chatrandom shagle collection 001 100",
		"2 4 tb videos chatroulette chatruletka ometv livhub video chat",
		"mf 16 holland omegle",
		"young girl has sex with a huge dog",
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
