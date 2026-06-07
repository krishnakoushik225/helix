package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds every Prometheus instrument used by Helix.
type Metrics struct {
	// RequestsTotal counts completed requests by provider, model, HTTP status,
	// and whether the response came from the semantic cache.
	RequestsTotal *prometheus.CounterVec

	// RequestDuration records end-to-end latency in seconds.
	RequestDuration *prometheus.HistogramVec

	// ActiveStreams tracks the number of open SSE connections.
	ActiveStreams prometheus.Gauge

	// TokensTotal counts tokens consumed, split by tenant, provider, and
	// direction ("input" / "output").
	TokensTotal *prometheus.CounterVec

	// CacheHitsTotal counts semantic cache hits.
	CacheHitsTotal prometheus.Counter

	// CostUSDTotal accumulates provider cost in USD by tenant and provider.
	CostUSDTotal *prometheus.CounterVec
}

// New registers all Helix instruments against the default Prometheus registry
// and returns the populated Metrics struct.
func New() *Metrics {
	return &Metrics{
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "helix_requests_total",
			Help: "Total inference requests completed, by provider, model, HTTP status, and cache_hit.",
		}, []string{"provider", "model", "status", "cache_hit"}),

		RequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "helix_request_duration_seconds",
			Help:    "End-to-end request latency in seconds.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
		}, []string{"provider", "model"}),

		ActiveStreams: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "helix_active_streams",
			Help: "Number of currently open SSE streaming connections.",
		}),

		TokensTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "helix_tokens_total",
			Help: "Tokens processed by direction (input/output), tenant, and provider.",
		}, []string{"tenant_id", "provider", "direction"}),

		CacheHitsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "helix_cache_hits_total",
			Help: "Total number of semantic cache hits served without calling a provider.",
		}),

		CostUSDTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "helix_cost_usd_total",
			Help: "Cumulative provider cost in USD, by tenant and provider.",
		}, []string{"tenant_id", "provider"}),
	}
}
