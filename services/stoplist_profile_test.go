package services

import (
	"os"
	"testing"

	sl "github.com/webtor-io/stoplist"
)

// Profile each phase separately to find the real bottleneck.
//
// Usage: STOPLIST_BENCH_YAML=... STOPLIST_BENCH_TORRENT=... \
//   go test -bench=BenchmarkPhases -benchmem -run='^$' ./services/

func BenchmarkPhases(b *testing.B) {
	yamlPath := os.Getenv("STOPLIST_BENCH_YAML")
	torrentPath := os.Getenv("STOPLIST_BENCH_TORRENT")
	if yamlPath == "" || torrentPath == "" {
		b.Skip("env not set")
	}
	checker, err := sl.NewRuleFromYamlFile(yamlPath)
	if err != nil {
		b.Fatal(err)
	}
	pf, err := newPrefilter(yamlPath)
	if err != nil {
		b.Fatal(err)
	}
	torrent, err := os.ReadFile(torrentPath)
	if err != nil {
		b.Fatal(err)
	}
	s := &Stoplist{c: checker, pf: pf}
	data, err := s.getData(torrent)
	if err != nil {
		b.Fatal(err)
	}
	// Pre-normalise once (out of the loop) so phases below skip it
	normalised := make([]string, len(data))
	for i, d := range data {
		normalised[i] = s.normalize(d)
	}
	b.Logf("data strings: %d", len(data))

	b.Run("getData", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = s.getData(torrent)
		}
	})
	b.Run("normalize_all", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, d := range data {
				_ = s.normalize(d)
			}
		}
	})
	b.Run("prefilter_only", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, d := range normalised {
				_ = pf.check(d)
			}
		}
	})
	b.Run("lib_check_only", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, d := range normalised {
				_ = checker.Check(d)
			}
		}
	})
}
