// Package privaterecord is the format of a per-account private tracker record:
// the announce tiers of one upload, normalized to the canonical JSON
// {"v":1,"t":[[url,…],…]}, are the plaintext sealed with AES-256-GCM as
// nonce(12) | ciphertext | tag(16) with the additional data ts1|<hash>|<uid>,
// and the registrable hosts of those trackers are kept beside it in the clear.
// Only torrent-store imports it. The key, the plaintext and the tracker URLs
// never reach a log line or an error returned from this package.
package privaterecord

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"
)

const (
	maxTrackers = 32
	maxURLLen   = 2048
)

var (
	ErrNoTracker       = errors.New("privaterecord: no usable tracker")
	ErrTooManyTrackers = errors.New("privaterecord: more than 32 trackers")
)

// usableURL reports whether raw is a valid UTF-8 http, https or udp URL with a
// host and returns the host as Hosts lists it: lower case, one trailing dot
// dropped, an IP literal in its canonical form, otherwise the registrable
// domain (eTLD+1), or the host itself when it is its own public suffix
// (co.uk, localhost). Invalid UTF-8 is refused because Canonical would turn it
// into U+FFFD, and a host with an empty label because it would reach Hosts whole.
func usableURL(raw string) (host string, ok bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch u.Scheme { // url.Parse lowercases the scheme
	case "http", "https", "udp":
	default:
		return "", false
	}
	// ponytail: no IDNA, so the punycode and unicode spellings of one tracker
	// are two hosts in the export.
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if h == "" {
		return "", false
	}
	// IP first: EffectiveTLDPlusOne leaves an IP literal alone only through
	// an undocumented check in PublicSuffix (without it 192.0.2.1 would come
	// out as 2.1), and addr.String() gives an IPv6 literal one spelling. The
	// IPv6 zone is free text from the URL, so it stays out of Hosts.
	if addr, err := netip.ParseAddr(h); err == nil {
		return addr.WithZone("").String(), true
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return d, true
	}
	// eTLD+1 also fails on an empty label (a..b, .a, a.), where h still has
	// the subdomain. A leading dot is checked apart: the PSL wildcard of a
	// TLD like *.ck makes .ck its own public suffix.
	if ps, _ := publicsuffix.PublicSuffix(h); ps == h && !strings.HasPrefix(h, ".") {
		return h, true
	}
	return "", false
}

// Normalize trims every URL, drops empty, duplicate (across tiers too),
// unusable and over-long URLs and the tiers left empty, keeping order and the
// URL text. It fails when nothing usable is left or more than 32 distinct URLs
// are.
func Normalize(tiers [][]string) ([][]string, error) {
	var out [][]string
	seen := map[string]bool{}
	for _, tier := range tiers {
		var kept []string
		for _, raw := range tier {
			s := strings.TrimSpace(raw)
			if s == "" || seen[s] || len(s) > maxURLLen {
				continue
			}
			if _, ok := usableURL(s); !ok {
				continue
			}
			seen[s] = true
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			out = append(out, kept)
		}
	}
	switch {
	case len(seen) == 0:
		return nil, ErrNoTracker
	case len(seen) > maxTrackers:
		return nil, ErrTooManyTrackers
	}
	return out, nil
}

// record is the plaintext shape, written by Canonical and read by Open.
type record struct {
	V int        `json:"v"`
	T [][]string `json:"t"`
}

// Canonical is the record plaintext for normalized tiers. HTML escaping is off
// so the URL bytes (&, <, >) are kept as they are.
func Canonical(tiers [][]string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(record{1, tiers}) // an int and strings always encode
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// Hosts lists the registrable host of every usable URL once, in order of first
// appearance. URLs without a usable host are skipped.
func Hosts(tiers [][]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tier := range tiers {
		for _, raw := range tier {
			h, ok := usableURL(raw)
			if !ok || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
