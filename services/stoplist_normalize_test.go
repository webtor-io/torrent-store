package services

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// Titles arrive in whatever Unicode form the uploader's OS produced.
// macOS file names are NFD (decomposed): "ñ" comes as "n"+U+0303, and
// the combining mark is neither \p{L} nor \d, so re1 used to shred
// "años" into "an os" — no diacritic token in the stoplist could ever
// match such input. normalize() must fold input to NFC first so both
// forms yield identical output.
func TestNormalizeUnicodeFormInsensitive(t *testing.T) {
	s := &Stoplist{}
	for _, nfc := range []string{
		"nude girls 9años collection",
		"flicka 9år samling",
	} {
		nfd := norm.NFD.String(nfc)
		if nfd == nfc {
			t.Fatalf("test setup: NFD(%q) did not change the string", nfc)
		}
		got, want := s.normalize(nfd), s.normalize(nfc)
		if got != want {
			t.Errorf("normalize(NFD)=%q, normalize(NFC)=%q — forms diverge", got, want)
		}
	}
}
