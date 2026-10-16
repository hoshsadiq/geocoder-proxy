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
	cacheLookups     *prom.CounterVec
	providerCalls    *prom.CounterVec
	providerLatency  *prom.HistogramVec
	quotaRemaining   *prom.GaugeVec
	lookupCalls      *prom.CounterVec
	providerTerminal *prom.CounterVec
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
		// Request-level outcome, independent of why providers failed:
		// sum(rate(geocoder_lookups_total{result="failed"}[5m]))
		lookupCalls: factory.NewCounterVec(prom.CounterOpts{
			Namespace: "geocoder",
			Name:      "lookups_total",
			Help:      "Lookups by result (ok, empty, failed).",
		}, []string{"result"}),
		// Terminal caps, a sub-count of provider errors:
		// sum by (provider) (rate(geocoder_provider_terminal_total[5m]))
		providerTerminal: factory.NewCounterVec(prom.CounterOpts{
			Namespace: "geocoder",
			Name:      "provider_terminal_total",
			Help:      "Provider responses that will not clear by retrying, by provider.",
		}, []string{"provider"}),
	}
}

// CacheLookup records one cache lookup with its outcome.
func (m *Metrics) CacheLookup(outcome port.CacheOutcome) {
	m.cacheLookups.WithLabelValues(string(outcome)).Inc()
}

// ProviderRequest records one upstream attempt. d is the call duration and is
// meaningless for outcomes that never reached the network; a zero d is treated
// as "no measurable attempt" and is counted without a latency observation.
func (m *Metrics) ProviderRequest(provider string, outcome port.ProviderOutcome, d time.Duration) {
	m.providerCalls.WithLabelValues(provider, string(outcome)).Inc()
	// Only outcomes that reached the network have a meaningful duration.
	if d > 0 && (outcome == port.ProviderSuccess || outcome == port.ProviderEmpty || outcome == port.ProviderError) {
		m.providerLatency.WithLabelValues(provider).Observe(d.Seconds())
	}
}

// QuotaRemaining reports the calls a provider has left today.
func (m *Metrics) QuotaRemaining(provider string, remaining int) {
	m.quotaRemaining.WithLabelValues(provider).Set(float64(remaining))
}

// Lookup records one lookup's result.
func (m *Metrics) Lookup(result port.LookupResult) {
	m.lookupCalls.WithLabelValues(string(result)).Inc()
}

// TerminalCap records one provider response that will not clear by retrying.
func (m *Metrics) TerminalCap(provider string) {
	m.providerTerminal.WithLabelValues(provider).Inc()
}
