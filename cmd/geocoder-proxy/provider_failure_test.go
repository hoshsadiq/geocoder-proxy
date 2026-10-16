package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/platform/config"
)

// cappedUpstream answers every request with a 429 carrying body and counts
// them, so a test can see how many times the router asked a capped provider.
func cappedUpstream(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func terminalCount(t *testing.T, reg *prom.Registry) int {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	total := 0
	for _, mf := range families {
		if mf.GetName() != "geocoder_provider_terminal_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			total += int(m.GetCounter().GetValue())
		}
	}
	return total
}

// TestTerminalCapFailsOverImmediately covers the incident: a provider at its
// plan cap answers 429 {"error":"limit_exceeded"}, is asked once, and the next
// provider answers, instead of being retried twice first.
func TestTerminalCapFailsOverImmediately(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	capped, calls := cappedUpstream(t, `{"error":"limit_exceeded"}`)
	answering := answeringUpstream(t)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout,
		providerAt(t, capped.URL), providerAt(t, answering.URL))

	rec := doRequest(c.handler, t.Context())

	must.Equal(http.StatusOK, rec.Code)
	is.Contains(rec.Body.String(), "Test Street")
	is.Equal(int32(1), calls.Load(), "a terminal cap is asked once, then the lookup fails over")
	is.Equal(1, terminalCount(t, c.reg), "the terminal cap is visible apart from the provider error outcome")
}

// TestAllProvidersTerminalCapIs503 covers the never-blank rule: every provider
// at its cap is an error, so the caller gets a JSON 503, never an empty 200 and
// never 502.
func TestAllProvidersTerminalCapIs503(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	first, firstCalls := cappedUpstream(t, `{"error":"limit_exceeded"}`)
	second, secondCalls := cappedUpstream(t, `{"error":"subscription_past_due"}`)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout,
		providerAt(t, first.URL), providerAt(t, second.URL))

	rec := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, rec.Code)
	is.JSONEq(`{"error":"geocoding temporarily unavailable"}`, rec.Body.String(), "a fixed JSON error body, never the provider's text")
	is.Equal(int32(1), firstCalls.Load())
	is.Equal(int32(1), secondCalls.Load())
	is.Equal(2, providerErrorCount(t, c.reg), "each capped provider is one error, no retries")
	is.Equal(2, terminalCount(t, c.reg), "each capped provider is one terminal cap")
}

func TestErrorBodyOn200Is503AndNotCached(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"code":"limit_exceeded"}}`))
	}))
	t.Cleanup(srv.Close)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout, providerAt(t, srv.URL))

	first := doRequest(c.handler, t.Context())
	second := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, first.Code)
	must.Equal(http.StatusServiceUnavailable, second.Code, "an error is not cached as an empty answer")
	is.Equal(int32(2), calls.Load(), "the second request asks upstream again")
	is.Equal(0, terminalCount(t, c.reg), "a 200 error is an ordinary error, not a 429 terminal cap")
}

func TestUnusableFeatureBodyIs503AndNotCached(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"limit_exceeded","features":[{"type":"Feature","properties":{"name":"Bad"},"geometry":{"type":"Point","coordinates":[999,999]}}]}`))
	}))
	t.Cleanup(srv.Close)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout, providerAt(t, srv.URL))

	first := doRequest(c.handler, t.Context())
	second := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, first.Code)
	must.Equal(http.StatusServiceUnavailable, second.Code, "an unusable feature is not cached as an empty answer")
	is.Equal(int32(2), calls.Load(), "the second request asks upstream again")
}

// TestOrdinaryFailureIsNotTerminal pins the negative direction: a failure that
// is not a terminal response must not touch the terminal counter.
func TestOrdinaryFailureIsNotTerminal(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	first, _ := cappedUpstream(t, `{"error":"rate_limited"}`)
	second, _ := cappedUpstream(t, `{"error":"rate_limited"}`)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout,
		providerAt(t, first.URL), providerAt(t, second.URL))

	rec := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, rec.Code)
	is.Equal(0, terminalCount(t, c.reg), "a non-terminal failure is not a terminal cap")
	is.Equal(2, providerErrorCount(t, c.reg))
}
