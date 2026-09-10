package services

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Placeholder tokens stand in for the real vocabulary: `foo bar` is a
// direct-block stopword, `alpha` an age marker, `beta` a sexual marker,
// `gamma` a name marker. The except list mirrors the production shape —
// release markers that sit next to an innocent word only because a
// track number, an episode number or a codec tag is there.
const exceptFixture = `
stopwords:
  - /\bfoo\s+bar\b/
age:
  - /\balpha\b/
sexual:
  - /\bbeta\b/
name:
  - /\bgamma\b/
main:
  - "{stopwords}"
  - "{age}+{sexual}"
  - "{age}+{name}"
except:
  - /\bflac\b/
  - /\bmp\s+3\b/
  - /\bs\s+\d{1,2}\s+e\s+\d{1,3}\b/
`

// Same rules, no except layer — must behave exactly as before.
const noExceptFixture = `
stopwords:
  - /\bfoo\s+bar\b/
age:
  - /\balpha\b/
sexual:
  - /\bbeta\b/
name:
  - /\bgamma\b/
main:
  - "{stopwords}"
  - "{age}+{sexual}"
  - "{age}+{name}"
`

func mustStoplist(t *testing.T, y string) *Stoplist {
	t.Helper()
	s, err := newStoplistFromYaml([]byte(y))
	if err != nil {
		t.Fatalf("newStoplistFromYaml: %v", err)
	}
	if s.pf == nil {
		t.Fatalf("prefilter did not compile for fixture")
	}
	return s
}

func TestStoplistExceptDiscardsStopwordsHit(t *testing.T) {
	s := mustStoplist(t, exceptFixture)
	for _, raw := range []string{
		"foo bar track flac",
		"Foo Bar - 03 - Track.mp3",
		"Foo.Bar.S01E10.1080p.mkv",
	} {
		before := testutil.ToFloat64(stoplistExceptedTotal)
		if cr := s.checkOne(raw); cr.Found {
			t.Errorf("checkOne(%q) = %v, want not found", raw, cr)
		}
		if got := testutil.ToFloat64(stoplistExceptedTotal) - before; got != 1 {
			t.Errorf("checkOne(%q): excepted counter delta = %v, want 1", raw, got)
		}
	}
}

func TestStoplistExceptKeepsStopwordsHitWithoutMarker(t *testing.T) {
	s := mustStoplist(t, exceptFixture)
	before := testutil.ToFloat64(stoplistExceptedTotal)
	cr := s.checkOne("foo bar track")
	if !cr.Found {
		t.Fatalf("checkOne(\"foo bar track\") not found, want stopwords hit")
	}
	if got := ruleLabel(cr); got != "stopwords" {
		t.Errorf("ruleLabel = %q, want stopwords (stack %v)", got, cr.Stack)
	}
	if got := testutil.ToFloat64(stoplistExceptedTotal) - before; got != 0 {
		t.Errorf("excepted counter delta = %v, want 0", got)
	}
}

func TestStoplistExceptNeverCoversComposite(t *testing.T) {
	s := mustStoplist(t, exceptFixture)
	for raw, want := range map[string]string{
		"alpha beta flac":  "age_sexual",
		"alpha gamma flac": "age_name",
		// Both the stopword and a composite fire; the stopword hit is
		// excepted, the composite must still block under its own label.
		"foo bar alpha beta flac": "age_sexual",
	} {
		cr := s.checkOne(raw)
		if !cr.Found {
			t.Errorf("checkOne(%q) not found, want composite hit", raw)
			continue
		}
		if got := ruleLabel(cr); got != want {
			t.Errorf("checkOne(%q): ruleLabel = %q, want %q (stack %v)", raw, got, want, cr.Stack)
		}
	}
}

func TestStoplistExceptParallelPathMatchesOneShot(t *testing.T) {
	s := mustStoplist(t, exceptFixture)
	filler := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		filler = append(filler, "innocent release name")
	}
	cases := []struct {
		name  string
		data  string
		found bool
		label string
	}{
		{"excepted", "foo bar track flac", false, ""},
		{"stopwords", "foo bar track", true, "stopwords"},
		{"composite", "alpha beta flac", true, "age_sexual"},
	}
	for _, c := range cases {
		data := append(append([]string{}, filler...), c.data)
		cr := s.checkParallel(data)
		if cr.Found != c.found {
			t.Errorf("%s: checkParallel found=%v, want %v (%v)", c.name, cr.Found, c.found, cr)
			continue
		}
		if c.found {
			if got := ruleLabel(cr); got != c.label {
				t.Errorf("%s: ruleLabel = %q, want %q", c.name, got, c.label)
			}
		}
	}
}

func TestStoplistWithoutExceptUnchanged(t *testing.T) {
	s := mustStoplist(t, noExceptFixture)
	before := testutil.ToFloat64(stoplistExceptedTotal)
	cr := s.checkOne("foo bar track flac")
	if !cr.Found {
		t.Fatalf("no except: \"foo bar track flac\" must still be blocked")
	}
	if got := ruleLabel(cr); got != "stopwords" {
		t.Errorf("ruleLabel = %q, want stopwords", got)
	}
	if got := testutil.ToFloat64(stoplistExceptedTotal) - before; got != 0 {
		t.Errorf("excepted counter delta = %v, want 0", got)
	}
	if cr := s.checkOne("clean flac rip"); cr.Found {
		t.Errorf("clean input blocked: %v", cr)
	}
}

func TestStoplistExceptInvalidRegexFailsClosed(t *testing.T) {
	for name, y := range map[string]string{
		"unclosed group": strings.Replace(exceptFixture, `/\bflac\b/`, `/(flac/`, 1),
		"not a lexeme":   strings.Replace(exceptFixture, `/\bflac\b/`, `flac`, 1),
	} {
		if _, err := newStoplistFromYaml([]byte(y)); err == nil {
			t.Errorf("%s: expected construction error, got nil", name)
		}
	}
}

// The prefilter is an early-reject over BLOCK patterns only. An except
// pattern must not survive into the alternation, otherwise every
// FLAC rip would pay for the full rule tree.
func TestPrefilterSkipsExceptPatterns(t *testing.T) {
	pf, err := newPrefilterFromYaml([]byte(exceptFixture))
	if err != nil {
		t.Fatalf("newPrefilterFromYaml: %v", err)
	}
	if pf == nil {
		t.Fatalf("prefilter is nil")
	}
	if pf.check("clean flac rip") {
		t.Errorf("prefilter fired on an except-only string")
	}
	if !pf.check("foo bar track") {
		t.Errorf("prefilter missed a block pattern")
	}
}
