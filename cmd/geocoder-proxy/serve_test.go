package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

	t.Run("unknown provider names the registered ones", func(t *testing.T) {
		_ = is
		_, err := buildProviders([]config.ProviderEntry{{Name: "bogus"}})
		must.Error(err)
		must.ErrorContains(err, "bogus")
		must.ErrorContains(err, "chibigeo")
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
