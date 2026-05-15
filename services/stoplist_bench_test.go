package services

import (
	"os"
	"testing"

	sl "github.com/webtor-io/stoplist"
)

// Bench setup: load production stoplist + a real heavyweight torrent
// (817KB, ~50 trackers, ~250 file paths). Run Check serially or in
// parallel-by-data and compare ns/op + throughput.
//
// Usage:
//   go test -bench=BenchmarkStoplistCheck -benchmem -count=3 ./services/
//
// Required env (provide before running):
//   STOPLIST_BENCH_YAML  — path to stoplist.yaml
//   STOPLIST_BENCH_TORRENT — path to a real .torrent file

func loadBenchStoplist(b *testing.B) *Stoplist {
	yaml := os.Getenv("STOPLIST_BENCH_YAML")
	if yaml == "" {
		b.Skip("STOPLIST_BENCH_YAML not set")
	}
	c, err := sl.NewRuleFromYamlFile(yaml)
	if err != nil {
		b.Fatalf("load stoplist: %v", err)
	}
	pf, err := newPrefilter(yaml)
	if err != nil {
		b.Fatalf("load prefilter: %v", err)
	}
	return &Stoplist{c: c, pf: pf}
}

func loadBenchTorrent(b *testing.B) []byte {
	path := os.Getenv("STOPLIST_BENCH_TORRENT")
	if path == "" {
		b.Skip("STOPLIST_BENCH_TORRENT not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read torrent: %v", err)
	}
	return data
}

func BenchmarkStoplistCheck(b *testing.B) {
	s := loadBenchStoplist(b)
	torrent := loadBenchTorrent(b)

	// Warm up: build the data slice once to confirm parse works.
	data, err := s.getData(torrent)
	if err != nil {
		b.Fatalf("getData: %v", err)
	}
	b.Logf("torrent has %d data strings (name + paths + trackers + comment + createdBy)", len(data))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := s.Check(torrent)
		if err != nil {
			b.Fatalf("Check: %v", err)
		}
	}
}
