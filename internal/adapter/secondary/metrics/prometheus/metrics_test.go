package prometheus

import (
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

func TestMetrics(t *testing.T) {
	is := assert.New(t)
	m := New(prom.NewRegistry())

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

	m.QuotaRemaining("chibigeo", 42)
	is.InDelta(42, testutil.ToFloat64(m.quotaRemaining.WithLabelValues("chibigeo")), 0.0001)
}
