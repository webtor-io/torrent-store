package services

import (
	"context"
	"errors"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Backend op outcomes. not_found is the normal answer from an upper tier
// that simply does not hold the torrent yet — the tier walk is built on it —
// so it must not be folded into error, or every cold pull would look like a
// Redis incident.
const (
	outcomeOK       = "ok"
	outcomeNotFound = "not_found"
	outcomeError    = "error"
)

// Manifest cache results, see getOrBuildDerived.
const (
	manifestHit     = "hit"     // a tier held a manifest and the caller accepted it
	manifestBuilt   = "built"   // no tier held one; built from the .torrent
	manifestRebuilt = "rebuilt" // a tier held one, the caller rejected it (stale stamp)
)

var (
	// grpcMetrics is the standard gRPC server instrumentation
	// (grpc_server_started_total, grpc_server_handled_total{grpc_code},
	// grpc_server_handling_seconds, msg counters), registered once on the
	// default registry that cs.NewProm already serves on /metrics.
	//
	// Bucket ceiling of 30s, not the 10s default: Pull answered from S3 is
	// the slow tail, and the graceful-stop budget is 15s — the histogram
	// has to resolve past both to show a stalled tier instead of clipping
	// into +Inf.
	grpcMetrics = grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}),
		),
	)

	// backendOpsTotal counts calls into each storage tier by operation and
	// outcome. The backend label is the provider's own Name() (badger,
	// redis, s3); op is the interface method, with derived reads/writes
	// suffixed by kind, so the set is fixed by the code, not the traffic.
	backendOpsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "torrent_store_backend_ops_total",
		Help: "Calls into a storage tier, by backend, operation and outcome (ok, not_found, error).",
	}, []string{"backend", "op", "outcome"})

	// backendOpSeconds is the latency of the same calls. Buckets reach 10s:
	// the HTTP client behind S3 times out at 10s, so anything past that is
	// the timeout itself.
	backendOpSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "torrent_store_backend_op_seconds",
		Help:    "Latency of calls into a storage tier, by backend and operation.",
		Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"backend", "op"})

	// manifestTotal counts how each manifest request was answered once it
	// reached the tiers. The in-process map in front of them singleflights
	// and holds a manifest for 5 minutes, so this is the rate of tier
	// lookups, not of Files RPCs. A rebuilt spike after a stoplist change
	// is expected; a rebuilt baseline is a stamp bug.
	manifestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "torrent_store_manifest_total",
		Help: "Manifest cache results at the tier layer: hit, built (miss), rebuilt (cached blob rejected).",
	}, []string{"result"})
)

func init() {
	// promauto registered the vectors above; the gRPC collector is a plain
	// Collector and has to be registered by hand.
	prometheus.MustRegister(grpcMetrics)
}

// opOutcome maps a provider call result onto a metric label.
func opOutcome(err error) string {
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, ErrNotFound):
		return outcomeNotFound
	}
	return outcomeError
}

// meteredProvider is a StoreProvider that records every call into the one
// it wraps. Wrapping at the interface is what keeps the Store's tier walk
// untouched: push, pull, touch, backfill and derived caching all reach a
// provider through these five methods, so one wrapper covers every path
// without a timer at each call site.
type meteredProvider struct {
	StoreProvider
}

// meterProviders wraps each provider so its calls are counted and timed.
func meterProviders(providers []StoreProvider) []StoreProvider {
	out := make([]StoreProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, meteredProvider{p})
	}
	return out
}

func (m meteredProvider) observe(op string, start time.Time, err error) {
	backend := m.Name()
	backendOpSeconds.WithLabelValues(backend, op).Observe(time.Since(start).Seconds())
	backendOpsTotal.WithLabelValues(backend, op, opOutcome(err)).Inc()
}

func (m meteredProvider) Push(ctx context.Context, h string, torrent []byte) (ok bool, err error) {
	start := time.Now()
	defer func() { m.observe("push", start, err) }()
	return m.StoreProvider.Push(ctx, h, torrent)
}

func (m meteredProvider) Pull(ctx context.Context, h string) (torrent []byte, err error) {
	start := time.Now()
	defer func() { m.observe("pull", start, err) }()
	return m.StoreProvider.Pull(ctx, h)
}

func (m meteredProvider) Touch(ctx context.Context, h string) (ok bool, err error) {
	start := time.Now()
	defer func() { m.observe("touch", start, err) }()
	return m.StoreProvider.Touch(ctx, h)
}

func (m meteredProvider) PushDerived(ctx context.Context, kind DerivedKind, h string, blob []byte) (ok bool, err error) {
	start := time.Now()
	defer func() { m.observe("push_"+string(kind), start, err) }()
	return m.StoreProvider.PushDerived(ctx, kind, h, blob)
}

func (m meteredProvider) PullDerived(ctx context.Context, kind DerivedKind, h string) (blob []byte, err error) {
	start := time.Now()
	defer func() { m.observe("pull_"+string(kind), start, err) }()
	return m.StoreProvider.PullDerived(ctx, kind, h)
}
