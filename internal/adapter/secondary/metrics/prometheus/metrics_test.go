package prometheus

import (
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// histogramSamples reads one provider's latency histogram _count and _sum, the
// only way to see whether an observation was recorded at all.
func histogramSamples(t *testing.T, reg *prom.Registry, provider string) (uint64, float64) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != "geocoder_provider_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "provider" && lp.GetValue() == provider {
					return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
				}
			}
		}
	}
	return 0, 0
}

func TestMetrics(t *testing.T) {
	is := assert.New(t)
	reg := prom.NewRegistry()
	m := New(reg)

	m.CacheLookup(port.CacheHit)
	m.CacheLookup(port.CacheHit)
	m.CacheLookup(port.CacheStale)
	is.InDelta(2, testutil.ToFloat64(m.cacheLookups.WithLabelValues("hit")), 0.0001)
	is.InDelta(1, testutil.ToFloat64(m.cacheLookups.WithLabelValues("stale")), 0.0001)
	is.InDelta(0, testutil.ToFloat64(m.cacheLookups.WithLabelValues("miss")), 0.0001)

	// Network outcomes record count and latency; the rest record only a count.
	m.ProviderRequest("chibigeo", port.ProviderSuccess, 150*time.Millisecond)
	m.ProviderRequest("chibigeo", port.ProviderQuotaExhausted, 0)
	is.InDelta(1, testutil.ToFloat64(m.providerCalls.WithLabelValues("chibigeo", "success")), 0.0001)
	is.InDelta(1, testutil.ToFloat64(m.providerCalls.WithLabelValues("chibigeo", "quota_exhausted")), 0.0001)
	is.Equal(1, testutil.CollectAndCount(m.providerLatency, "geocoder_provider_request_duration_seconds"))

	// An error with no measurable attempt (a budget cut) is counted without a
	// latency observation, so the histogram is not skewed by zeroes.
	countBefore, sumBefore := histogramSamples(t, reg, "chibigeo")
	m.ProviderRequest("chibigeo", port.ProviderError, 0)
	is.InDelta(1, testutil.ToFloat64(m.providerCalls.WithLabelValues("chibigeo", "error")), 0.0001)
	countAfter, sumAfter := histogramSamples(t, reg, "chibigeo")
	is.Equal(countBefore, countAfter, "a zero-duration error must not add an observation")
	is.InDelta(sumBefore, sumAfter, 0.0001, "a zero-duration error must not add to the latency sum")

	// A failed lookup is visible at the request level, whatever the providers
	// did, so an outage that never records an error outcome still alerts.
	m.Lookup(port.LookupOK)
	m.Lookup(port.LookupFailed)
	is.InDelta(1, testutil.ToFloat64(m.lookupCalls.WithLabelValues("ok")), 0.0001)
	is.InDelta(1, testutil.ToFloat64(m.lookupCalls.WithLabelValues("failed")), 0.0001)
	is.InDelta(0, testutil.ToFloat64(m.lookupCalls.WithLabelValues("empty")), 0.0001)

	m.QuotaRemaining("chibigeo", 42)
	is.InDelta(42, testutil.ToFloat64(m.quotaRemaining.WithLabelValues("chibigeo")), 0.0001)
}
