package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
}
