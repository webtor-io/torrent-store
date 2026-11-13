package privaterecord

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// tiersOf returns n single-URL tiers http://tracker.example/<i>.
func tiersOf(n int) [][]string {
	t := make([][]string, n)
	for i := range t {
		t[i] = []string{"http://tracker.example/" + strconv.Itoa(i)}
	}
	return t
}

func TestNormalize(t *testing.T) {
	const u = "http://tracker.example/a"
	long := "https://tracker.example/" + strings.Repeat("a", 2049-len("https://tracker.example/")) // 2049 bytes
	exact := long[:2048]
	cases := []struct {
		name string
		in   [][]string
		want [][]string
		err  error
	}{
		{"trims, keeps order", [][]string{{" udp://tracker.example:6969/announce ", "https://tracker.example/b"}}, [][]string{{"udp://tracker.example:6969/announce", "https://tracker.example/b"}}, nil},
		{"drops empty urls and tiers", [][]string{{"", "  "}, {u}}, [][]string{{u}}, nil},
		{"drops duplicates within and across tiers", [][]string{{u, u}, {u, "udp://tracker.example:1/b"}}, [][]string{{u}, {"udp://tracker.example:1/b"}}, nil},
		{"only http https udp with host", [][]string{{"wss://tracker.example/a", "ftp://tracker.example/a", "http:///announce", "udp://:6969", "http://./a", "magnet:?xt=urn:btih:00",
			"https://u0000..tracker.example/a", "https://.u0000.tracker.example/a", "udp://u0000.tracker.example..:6969/a", "http://../a", "https://.ck/a",
			"http://tracker.example/a\xff", u}}, [][]string{{u}}, nil},
		{"scheme case-insensitive, bytes kept", [][]string{{"HTTP://tracker.example/a"}}, [][]string{{"HTTP://tracker.example/a"}}, nil},
		{"over 2048 bytes dropped", [][]string{{long, u}}, [][]string{{u}}, nil},
		{"2048 bytes kept", [][]string{{exact}}, [][]string{{exact}}, nil},
		{"nothing usable", [][]string{{"", "ftp://tracker.example/a"}}, nil, ErrNoTracker},
		{"nil", nil, nil, ErrNoTracker},
		{"32", tiersOf(32), tiersOf(32), nil},
		{"33", tiersOf(33), nil, ErrTooManyTrackers},
		{"duplicates do not count", append(tiersOf(32), tiersOf(32)...), tiersOf(32), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.in)
			if !errors.Is(err, c.err) || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Normalize = %q, %v; want %q, %v", got, err, c.want, c.err)
			}
		})
	}
}

func TestCanonicalKeepsURLBytes(t *testing.T) {
	got := string(Canonical([][]string{{"https://tracker.example/a?x=<0>&y=0"}, {"udp://tracker.example:1/b"}}))
	want := `{"v":1,"t":[["https://tracker.example/a?x=<0>&y=0"],["udp://tracker.example:1/b"]]}`
	if got != want {
		t.Fatalf("Canonical = %s, want %s", got, want)
	}
}

func TestHosts(t *testing.T) {
	got := Hosts([][]string{
		{"https://u0000.tracker.example/announce?passkey=00000000", "udp://Tracker.Example.:6969/announce"},
		{"http://user:pw@a.b.tracker.example:8080/x", "udp://192.0.2.1:6969/a", "udp://[2001:DB8::1]:6969/a",
			"udp://[2001:db8:0:0:0:0:0:1]:6969/a", "http://other.invalid/a", "http://localhost/a", "http://co.uk/a"},
	})
	want := []string{"tracker.example", "192.0.2.1", "2001:db8::1", "other.invalid", "localhost", "co.uk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Hosts = %q, want %q", got, want)
	}
}
