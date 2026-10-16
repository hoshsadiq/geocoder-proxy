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

// cappedUpstream answers every request with a 429 carrying body.
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

func TestAllProvidersTerminalCapIs503(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	first, firstCalls := cappedUpstream(t, `{"error":"limit_exceeded"}`)
	second, secondCalls := cappedUpstream(t, `{"error":"limit_exceeded"}`)
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
		_, _ = w.Write([]byte(`{"error":"limit_exceeded"}`))
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

func TestNonStringErrorIs503AndNotCached(t *testing.T) {
	feature := `{"type":"Feature","properties":{"name":"Good"},"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}`
	tests := []struct {
		name string
		body string
	}{
		{name: "false with no features", body: `{"error":false}`},
		{name: "zero with no features", body: `{"error":0}`},
		{name: "empty object with no features", body: `{"error":{}}`},
		{name: "empty array with no features", body: `{"error":[]}`},
		{name: "number with a usable feature", body: `{"error":42,"features":[` + feature + `]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			calls := &atomic.Int32{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout, providerAt(t, srv.URL))

			first := doRequest(c.handler, t.Context())
			second := doRequest(c.handler, t.Context())

			must.Equal(http.StatusServiceUnavailable, first.Code)
			must.Equal(http.StatusServiceUnavailable, second.Code, "a decode failure is not cached as an empty answer")
			is.Equal(int32(2), calls.Load(), "the second request asks upstream again")
		})
	}
}

// TestDecodeFailureLogsNoUpstreamText pins the PII rule on the decode path.
// TestTerminalCapIs503AndNotCached pins that a terminal cap is neither cached
// nor echoed: the cap body carries a key and coordinates, and the WARN line
// names our own code only.
func TestTerminalCapIs503AndNotCached(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv, calls := cappedUpstream(t, `{"error":"limit_exceeded","debug":"REALKEY123 lat=51.4774"}`)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout, providerAt(t, srv.URL))

	first := doRequest(c.handler, t.Context())
	second := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, first.Code)
	must.Equal(http.StatusServiceUnavailable, second.Code, "a terminal cap is not cached")
	is.Equal(int32(2), calls.Load(), "the second request asks upstream again")
	logs := c.logs.String()
	is.Contains(logs, "terminal provider failure): limit_exceeded")
	is.NotContains(logs, "REALKEY123", "a credential must never reach a log")
	is.NotContains(logs, "51.4774", "a coordinate must never reach a log")
}

func TestDecodeFailureLogsNoUpstreamText(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[{"type":"Feature","properties":{"name":"X","osm_id":"REALKEY123 lat=51.4774"},"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}]}`))
	}))
	t.Cleanup(srv.Close)
	c := newChain(t, config.DefaultRequestTimeout, config.DefaultUpstreamTimeout, providerAt(t, srv.URL))

	rec := doRequest(c.handler, t.Context())

	must.Equal(http.StatusServiceUnavailable, rec.Code)
	logs := c.logs.String()
	is.Contains(logs, "decoding upstream response", "the decode failure is logged")
	is.NotContains(logs, "REALKEY123", "a credential must never reach a log")
	is.NotContains(logs, "51.4774", "a coordinate must never reach a log")
	is.NotContains(rec.Body.String(), "REALKEY123")
	is.NotContains(rec.Body.String(), "51.4774")
}

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
