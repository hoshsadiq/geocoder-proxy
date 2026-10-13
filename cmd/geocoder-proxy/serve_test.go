package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/platform/config"
)

// TestServeEndToEnd boots the real composition root against a fake upstream
// and exercises the whole path: config from env, registry, router, cache,
// both listeners, graceful shutdown.
func TestServeEndToEnd(t *testing.T) {
	is, must := assert.New(t), require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[{"type":"Feature",
			"properties":{"osm_id":1,"osm_type":"W","osm_key":"highway","osm_value":"residential",
			"name":"Test Street","street":"Test Street","city":"Testville","country":"Testland","countrycode":"GB"},
			"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}]}`)
	}))
	defer upstream.Close()

	appAddr := freeAddr(t)
	opsAddr := freeAddr(t)
	t.Setenv("GEOCODER_LISTEN_ADDR", appAddr)
	t.Setenv("GEOCODER_OPS_ADDR", opsAddr)
	t.Setenv("GEOCODER_PROVIDERS", "chibigeo")
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_BASE_URL", upstream.URL)
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_RATE_PER_SECOND", "10")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, "") }()

	base := "http://" + appAddr
	// The ops listener answers once the server is up.
	get := func(url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}

	must.Eventually(func() bool {
		resp, err := get("http://" + opsAddr + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "ops listener never came up")

	resp, err := get(base + "/reverse?lat=51.4774&lon=-0.061")
	must.NoError(err)
	defer resp.Body.Close()
	must.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Type     string `json:"type"`
		Features []struct {
			Properties struct {
				Name    string `json:"name"`
				Country string `json:"country"`
			} `json:"properties"`
		} `json:"features"`
	}
	must.NoError(json.NewDecoder(resp.Body).Decode(&body))
	is.Equal("FeatureCollection", body.Type)
	must.Len(body.Features, 1)
	is.Equal("Test Street", body.Features[0].Properties.Name)
	is.Equal("United Kingdom", body.Features[0].Properties.Country, "country canonicalised from the code")

	metrics, err := get("http://" + opsAddr + "/metrics")
	must.NoError(err)
	_ = metrics.Body.Close()
	must.Equal(http.StatusOK, metrics.StatusCode)

	cancel()
	must.NoError(<-done, "graceful shutdown")
}

// TestServeRotatesAcrossInstances boots the real composition root with two
// instances of one provider type, pointed at two fake upstreams, and checks
// that successive lookups land on different instances and that the provider
// metric label carries the instance names.
func TestServeRotatesAcrossInstances(t *testing.T) {
	is, must := assert.New(t), require.New(t)

	newUpstream := func(hits *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"type":"FeatureCollection","features":[{"type":"Feature",
				"properties":{"osm_id":1,"osm_type":"W","name":"Test Street","street":"Test Street","city":"Testville","country":"Testland","countrycode":"GB"},
				"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}]}`)
		}))
	}
	var euHits, usHits atomic.Int64
	eu, us := newUpstream(&euHits), newUpstream(&usHits)
	defer eu.Close()
	defer us.Close()

	appAddr := freeAddr(t)
	opsAddr := freeAddr(t)
	t.Setenv("GEOCODER_LISTEN_ADDR", appAddr)
	t.Setenv("GEOCODER_OPS_ADDR", opsAddr)
	t.Setenv("GEOCODER_PROVIDERS", "chibi-eu,chibi-us")
	// Both instances share the type; only the instance name tells them apart.
	t.Setenv("GEOCODER_PROVIDER_CHIBI_EU_TYPE", "chibigeo")
	t.Setenv("GEOCODER_PROVIDER_CHIBI_EU_BASE_URL", eu.URL)
	t.Setenv("GEOCODER_PROVIDER_CHIBI_EU_RATE_PER_SECOND", "10")
	t.Setenv("GEOCODER_PROVIDER_CHIBI_US_TYPE", "chibigeo")
	t.Setenv("GEOCODER_PROVIDER_CHIBI_US_BASE_URL", us.URL)
	t.Setenv("GEOCODER_PROVIDER_CHIBI_US_RATE_PER_SECOND", "10")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, "") }()
	defer func() {
		cancel()
		must.NoError(<-done, "graceful shutdown")
	}()

	get := func(url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	must.Eventually(func() bool {
		resp, err := get("http://" + opsAddr + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "ops listener never came up")

	// Two distinct cells, so the cache cannot serve the second lookup: rotation
	// should move the second one to the other instance.
	for _, q := range []string{"lat=51.4774&lon=-0.061", "lat=51.5000&lon=-0.1000"} {
		resp, err := get("http://" + appAddr + "/reverse?" + q)
		must.NoError(err)
		_ = resp.Body.Close()
		must.Equal(http.StatusOK, resp.StatusCode)
	}
	is.Equal(int64(1), euHits.Load(), "the first lookup starts at chibi-eu")
	is.Equal(int64(1), usHits.Load(), "the second lookup starts at chibi-us")

	metrics, err := get("http://" + opsAddr + "/metrics")
	must.NoError(err)
	defer metrics.Body.Close()
	body, err := io.ReadAll(metrics.Body)
	must.NoError(err)
	text := string(body)
	is.Contains(text, `provider="chibi-eu"`, "the instance name is the provider label")
	is.Contains(text, `provider="chibi-us"`)
	is.NotContains(text, `provider="chibigeo"`, "the type is not the label when an instance name is set")
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

func TestBuildProviders(t *testing.T) {
	is, must := assert.New(t), require.New(t)

	t.Run("valid entry builds through the registry", func(t *testing.T) {
		providers, err := buildProviders([]config.ProviderEntry{{
			Name: "chibigeo",
			Settings: map[string]any{
				"base_url":        "http://127.0.0.1",
				"rate_per_second": "2.5",
				"daily_quota":     "100",
			},
		}})
		must.NoError(err)
		must.Len(providers, 1)
		is.Equal("chibigeo", providers[0].Geocoder.Name())
		is.Equal(100, providers[0].DailyQuota)
		is.NotNil(providers[0].Limiter)
	})

	t.Run("two instances of one type keep their own names", func(t *testing.T) {
		settings := func(baseURL string) map[string]any {
			return map[string]any{
				"base_url":        baseURL,
				"rate_per_second": "1",
				"daily_quota":     "2500",
			}
		}
		providers, err := buildProviders([]config.ProviderEntry{
			{Name: "chibi-eu", Type: "chibigeo", Tier: 1, Settings: settings("http://eu.example")},
			{Name: "chibi-us", Type: "chibigeo", Tier: 2, Settings: settings("http://us.example")},
		})
		must.NoError(err)
		must.Len(providers, 2)
		is.Equal("chibi-eu", providers[0].Name)
		is.Equal(1, providers[0].Tier)
		is.Equal("chibigeo", providers[0].Geocoder.Name(), "both instances share the registry type")
		is.Equal("chibi-us", providers[1].Name)
		is.Equal(2, providers[1].Tier)
		is.NotSame(providers[0].Geocoder, providers[1].Geocoder, "each instance builds its own client")
	})

	t.Run("unknown provider names the registered ones", func(t *testing.T) {
		_ = is
		_, err := buildProviders([]config.ProviderEntry{{Name: "bogus"}})
		must.Error(err)
		must.ErrorContains(err, "bogus")
		must.ErrorContains(err, "chibigeo")
	})

	t.Run("unknown type names the instance and is reported with the others", func(t *testing.T) {
		_, err := buildProviders([]config.ProviderEntry{
			{Name: "chibi-eu", Type: "chibigeo", Settings: map[string]any{"base_url": "not a url", "rate_per_second": "1"}},
			{Name: "mystery", Type: "bogus", Settings: map[string]any{"rate_per_second": "1"}},
		})
		must.Error(err)
		must.ErrorContains(err, "chibi-eu", "the instance with the bad URL is reported")
		must.ErrorContains(err, "base_url")
		must.ErrorContains(err, "unknown type \"bogus\"", "the unknown type is reported in the same pass")
	})

	t.Run("a typo'd key is rejected, not ignored", func(t *testing.T) {
		_, err := buildProviders([]config.ProviderEntry{{
			Name:     "chibigeo",
			Settings: map[string]any{"rate_per_secnd": "1"},
		}})
		must.Error(err)
		must.ErrorContains(err, "rate_per_secnd")
	})

	t.Run("fractional quota is refused, not truncated", func(t *testing.T) {
		_, err := buildProviders([]config.ProviderEntry{{
			Name:     "chibigeo",
			Settings: map[string]any{"rate_per_second": "1", "daily_quota": "1.5"},
		}})
		must.Error(err)
		must.ErrorContains(err, "whole number")
	})

	t.Run("every provider's problems are reported together", func(t *testing.T) {
		_, err := buildProviders([]config.ProviderEntry{
			{Name: "geoapify", Settings: map[string]any{"rate_per_second": "1"}},
			{Name: "nominatim", Settings: map[string]any{"rate_per_second": "1"}},
		})
		must.Error(err)
		must.ErrorContains(err, "api_key is required")
		must.ErrorContains(err, "user_agent is required")
	})

	t.Run("a missing rate is caught by the provider", func(t *testing.T) {
		_, err := buildProviders([]config.ProviderEntry{{
			Name:     "chibigeo",
			Settings: map[string]any{"base_url": "http://127.0.0.1"},
		}})
		must.Error(err)
		must.ErrorContains(err, "rate_per_second")
	})
}
