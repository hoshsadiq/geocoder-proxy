package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language/display"

	photonprimary "github.com/hoshsadiq/geocoder-proxy/internal/adapter/primary/photon"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/cache/memory"
	metricsadp "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/metrics/prometheus"
	providerphoton "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/photon"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/service"
)

// featureCollectionJSON is one Photon-shaped answer, enough for the router
// to treat the lookup as found.
const featureCollectionJSON = `{"type":"FeatureCollection","features":[{"type":"Feature",` +
	`"properties":{"osm_id":1,"osm_type":"W","name":"Test Street","street":"Test Street",` +
	`"city":"Testville","country":"Testland","countrycode":"GB"},` +
	`"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}]}`

const reversePath = "/reverse?lat=51.4774&lon=-0.061"

func answeringUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(featureCollectionJSON))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hangingUpstream never answers: it blocks until the provider client gives up.
func hangingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func providerAt(t *testing.T, baseURL string) service.Provider {
	t.Helper()
	client, err := providerphoton.New(&providerphoton.Config{BaseURL: baseURL, RatePerSecond: 1000})
	require.NoError(t, err)
	return service.Provider{Geocoder: client}
}

type chain struct {
	handler http.Handler
	reg     *prom.Registry
	logs    *bytes.Buffer
}

// newChain builds the real request path: the deadline middleware, the Photon
// handler, the real router and real provider clients, with only the upstream
// swapped for an httptest server.
func newChain(t *testing.T, budget, attempt time.Duration, providers ...service.Provider) chain {
	t.Helper()
	reg := prom.NewRegistry()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	router := service.New(memory.New(time.Hour), metricsadp.New(reg), providers, service.WithAttemptTimeout(attempt))
	handler := photonprimary.New(router, display.English.Regions(), logger)
	return chain{handler: withRequestDeadline(handler, budget), reg: reg, logs: logs}
}

// providerErrorCount sums the error-outcome counter, the series the error-rate
// alert reads.
func providerErrorCount(t *testing.T, reg *prom.Registry) int {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	total := 0
	for _, mf := range families {
		if mf.GetName() != "geocoder_provider_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "outcome" && lp.GetValue() == string(port.ProviderError) {
					total += int(m.GetCounter().GetValue())
				}
			}
		}
	}
	return total
}

func doRequest(handler http.Handler, ctx context.Context) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, reversePath, nil))
	return rec
}

// TestRequestDeadlineReturns503 covers the budget expiry: every provider hangs,
// the budget runs out, and the caller gets a JSON 503, not an empty reply and
// never 502. It also pins that the expiry is counted as an error and logged at
// WARN.
func TestRequestDeadlineReturns503(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	hanging := hangingUpstream(t)
	c := newChain(t, 300*time.Millisecond, 150*time.Millisecond,
		providerAt(t, hanging.URL), providerAt(t, hanging.URL))

	start := time.Now()
	rec := doRequest(c.handler, t.Context())
	elapsed := time.Since(start)

	must.Equal(http.StatusServiceUnavailable, rec.Code, "a budget expiry is a 503, not an empty 200")
	must.Less(elapsed, 2*time.Second, "the answer lands within the budget plus a small margin")
	is.Contains(rec.Header().Get("Content-Type"), "application/json")

	var body map[string]string
	must.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	is.NotEmpty(body["error"], "a JSON error body, never an empty reply")
	is.NotContains(rec.Body.String(), "51.4774", "error bodies never carry coordinates")

	is.Positive(providerErrorCount(t, c.reg), "a deadline-exceeded lookup is counted as error")
	is.Contains(c.logs.String(), "request budget exceeded")
	is.NotContains(c.logs.String(), `"level":"ERROR"`, "a budget expiry is a WARN, not an error")
}

// TestRequestDeadlineFailsOverWithinBudget covers failover: the hanging
// provider is capped, the healthy one answers, and the caller still gets an
// answer inside the budget.
func TestRequestDeadlineFailsOverWithinBudget(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	hanging := hangingUpstream(t)
	answering := answeringUpstream(t)
	c := newChain(t, 3*time.Second, 200*time.Millisecond,
		providerAt(t, hanging.URL), providerAt(t, answering.URL))

	start := time.Now()
	rec := doRequest(c.handler, t.Context())
	elapsed := time.Since(start)

	must.Equal(http.StatusOK, rec.Code, "a healthy provider answers after the slow one is capped")
	must.Less(elapsed, 3*time.Second, "the answer lands inside the budget")
	is.Contains(rec.Body.String(), "Test Street")
}

// TestRequestDeadlineReturnsAnswerBeforeBudget covers the completed answer: a
// result obtained before the budget expires is a plain 200.
func TestRequestDeadlineReturnsAnswerBeforeBudget(t *testing.T) {
	must := require.New(t)
	answering := answeringUpstream(t)
	c := newChain(t, time.Second, 500*time.Millisecond, providerAt(t, answering.URL))

	rec := doRequest(c.handler, t.Context())
	must.Equal(http.StatusOK, rec.Code)
	must.Contains(rec.Body.String(), "Test Street")
}

// TestClientCancellationWritesNothing covers the departed caller: nothing is
// written at all, and it is not logged as an error. The naive
// r.Context().Err() != nil check would pass this but fail the deadline test
// above, which is the bug this change guards against.
func TestClientCancellationWritesNothing(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	hanging := hangingUpstream(t)
	c := newChain(t, 5*time.Second, 4*time.Second, providerAt(t, hanging.URL))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := doRequest(c.handler, ctx)

	must.Empty(rec.Body.String(), "a departed caller gets no body")
	is.Empty(rec.Header().Get("Content-Type"), "a departed caller gets no status")
	is.NotContains(c.logs.String(), `"level":"ERROR"`, "a departed caller is not an error")
}
