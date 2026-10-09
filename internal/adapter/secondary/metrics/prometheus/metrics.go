// Package prometheus is the Metrics adapter. Label cardinality stays bounded
// by construction: only provider names and the port package's outcome
// constants become labels, never coordinates or query text.
package prometheus

import (
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Compile-time guard.
var _ port.Metrics = (*Metrics)(nil)

// Metrics implements port.Metrics on a Prometheus registry.
type Metrics struct {
	cacheLookups    *prom.CounterVec
	providerCalls   *prom.CounterVec
	providerLatency *prom.HistogramVec
	quotaRemaining  *prom.GaugeVec
}

// New registers the metrics on reg (pass prom.DefaultRegisterer in
// production, a fresh prom.NewRegistry() in tests).
func New(reg prom.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		// Cache effectiveness: sum(rate(geocoder_cache_lookups_total{result="hit"}[5m]))
		//   / sum(rate(geocoder_cache_lookups_total[5m]))
		cacheLookups: factory.NewCounterVec(prom.CounterOpts{
			Namespace: "geocoder",
			Name:      "cache_lookups_total",
			Help:      "Cache lookups by outcome (hit, miss, stale).",
		}, []string{"result"}),
		// Provider health: sum by (provider, outcome) (rate(geocoder_provider_requests_total[5m]))
		providerCalls: factory.NewCounterVec(prom.CounterOpts{
			Namespace: "geocoder",
			Name:      "provider_requests_total",
			Help:      "Upstream provider attempts by provider and outcome.",
		}, []string{"provider", "outcome"}),
		// Provider latency: histogram_quantile(0.95,
		//   sum by (provider, le) (rate(geocoder_provider_request_duration_seconds_bucket[5m])))
		providerLatency: factory.NewHistogramVec(prom.HistogramOpts{
			Namespace: "geocoder",
			Name:      "provider_request_duration_seconds",
			Help:      "Upstream provider call latency in seconds.",
			Buckets:   prom.DefBuckets,
		}, []string{"provider"}),
		// Quota burn: geocoder_provider_quota_remaining
		quotaRemaining: factory.NewGaugeVec(prom.GaugeOpts{
			Namespace: "geocoder",
			Name:      "provider_quota_remaining",
			Help:      "Calls left against the provider's daily quota, when one is configured.",
		}, []string{"provider"}),
	}
}

// CacheLookup records one cache lookup with its outcome.
func (m *Metrics) CacheLookup(outcome port.CacheOutcome) {
	m.cacheLookups.WithLabelValues(string(outcome)).Inc()
}

// ProviderRequest records one upstream attempt.
func (m *Metrics) ProviderRequest(provider string, outcome port.ProviderOutcome, d time.Duration) {
	m.providerCalls.WithLabelValues(provider, string(outcome)).Inc()
	// Only outcomes that reached the network have a meaningful duration.
	if outcome == port.ProviderSuccess || outcome == port.ProviderEmpty || outcome == port.ProviderError {
		m.providerLatency.WithLabelValues(provider).Observe(d.Seconds())
	}
}

// QuotaRemaining reports the calls a provider has left today.
func (m *Metrics) QuotaRemaining(provider string, remaining int) {
	m.quotaRemaining.WithLabelValues(provider).Set(float64(remaining))
}
