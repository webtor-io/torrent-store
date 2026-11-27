package services

import (
	"strings"
	"testing"
)

// Placeholder tokens as in stoplist_except_test.go: `<n> alpha` is a
// direct age marker on line 0, `<n> years` an age, `beta` a sexual
// marker, `gamma` a name marker, `foo bar` a hard stopword. The masks
// stand for a title whose number is an episode or a year count.
const maskRules = `
ambiguous_stopwords:
  - /\b\d+\s+alpha\b/
age:
  - /\b\d+\s+years?\b/
sexual:
  - /\bbeta\b/
name:
  - /\bgamma\b/
stopwords:
  - /\bfoo\s+bar\b/
  - /\bova\s+2\s+alpha\s+the\s+return\s+x\b/
main:
  - "{ambiguous_stopwords}"
  - "{age}+{sexual}"
  - "{age}+{name}"
  - "{stopwords}"
`

const maskSection = `
mask:
  - /\bsample show ova (0*[12]) alpha the return\b/
  - /\bsample band (7) years? song\b.*\b(?:mp 3|flac)\s*$/
`

func TestStoplistMask(t *testing.T) {
	with := mustStoplist(t, maskRules+maskSection)
	without := mustStoplist(t, maskRules)
	for _, c := range []struct {
		raw   string
		block bool
	}{
		{"Sample Show OVA 02 alpha the return.mkv", false},
		{"Sample Band - 7 years song - beta.flac", false},
		{"Sample Band - 7 years song - beta.mp3", false},
		// the title again in the file name: every copy is masked
		{"Sample Band - 7 years song - beta/01 Sample Band - 7 years song.mp3", false},
		// the music mask needs an audio file
		{"Sample Band - 7 years song - beta.mkv", true},
		// another age in the same string still blocks
		{"Sample Show OVA 2 alpha the return - 12 alpha.mkv", true},
		{"Sample Show OVA 2 alpha the return beta 9 years.mkv", true},
		{"Sample Show OVA 2 alpha the return gamma.mkv", false},
		{"Sample Show OVA 2 alpha the return 9 years gamma.mkv", true},
		// a hard line sees the unmasked string
		{"Sample Show OVA 2 alpha the return x.mkv", true},
		{"Sample Show OVA 2 alpha the return foo bar.mkv", true},
		// a number outside the closed list is not masked
		{"Sample Show OVA 3 alpha the return.mkv", true},
	} {
		if got := with.checkOne(c.raw).Found; got != c.block {
			t.Errorf("with mask: checkOne(%q) = %v, want %v", c.raw, got, c.block)
		}
		if !without.checkOne(c.raw).Found {
			t.Errorf("without mask: checkOne(%q) passed; the case tests nothing", c.raw)
		}
	}
}

func TestStoplistMaskRefusesBadRuleFile(t *testing.T) {
	for name, y := range map[string]string{
		"bare word":      maskRules + "mask:\n  - sample show\n",
		"no group":       maskRules + "mask:\n  - /\\bsample show ova 2\\b/\n",
		"two groups":     maskRules + "mask:\n  - /\\b(sample) show ova (2)\\b/\n",
		"unknown layout": strings.Replace(maskRules, `  - "{age}+{name}"`+"\n", "", 1) + maskSection,
	} {
		if _, err := newStoplistFromYaml([]byte(y)); err == nil {
			t.Errorf("%s: newStoplistFromYaml accepted the rule file", name)
		}
	}
}
