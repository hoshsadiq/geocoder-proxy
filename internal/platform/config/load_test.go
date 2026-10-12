package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadYAML(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
listen_addr: ":9000"
cache:
  ttl: 48h
  max_reuse_distance: 50
providers:
  - name: chibigeo
    api_key: from-file
    rate_per_second: 1
    daily_quota: 2500
`)

	cfg, err := Load(path, nil)
	must.NoError(err)
	is.Equal(":9000", cfg.ListenAddr)
	is.Equal(48*time.Hour, cfg.Cache.TTL)
	is.InDelta(50, cfg.Cache.MaxReuseDistance, 0.0001)
	must.Len(cfg.Providers, 1)
	is.Equal("from-file", cfg.Providers[0].Settings["api_key"])
}

func TestEnvOverridesFile(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
providers:
  - name: chibigeo
    rate_per_second: 1
`)
	t.Setenv("GEOCODER_LISTEN_ADDR", ":7777")
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_API_KEY", "from-env")
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_DAILY_QUOTA", "100")

	cfg, err := Load(path, nil)
	must.NoError(err)
	is.Equal(":7777", cfg.ListenAddr)
	is.Equal("from-env", cfg.Providers[0].Settings["api_key"])
	is.Equal("100", cfg.Providers[0].Settings["daily_quota"])
}

func TestProvidersEnvSelectsAndOrders(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
providers:
  - name: chibigeo
    rate_per_second: 1
  - name: geoapify
    rate_per_second: 5
    api_key: x
`)
	t.Setenv("GEOCODER_PROVIDERS", "geoapify,chibigeo")

	cfg, err := Load(path, nil)
	must.NoError(err)
	must.Len(cfg.Providers, 2)
	is.Equal("geoapify", cfg.Providers[0].Name)
	is.Equal("chibigeo", cfg.Providers[1].Name)
}

func TestProviderFromEnvOnly(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	t.Setenv("GEOCODER_PROVIDERS", "nominatim")
	t.Setenv("GEOCODER_PROVIDER_NOMINATIM_USER_AGENT", "test-agent")
	t.Setenv("GEOCODER_PROVIDER_NOMINATIM_RATE_PER_SECOND", "1")

	cfg, err := Load("", nil)
	must.NoError(err)
	must.Len(cfg.Providers, 1)
	is.Equal("nominatim", cfg.Providers[0].Name)
	is.Equal("test-agent", cfg.Providers[0].Settings["user_agent"])
	is.Equal("1", cfg.Providers[0].Settings["rate_per_second"])
}

func TestInvalidTTLError(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, "cache:\n  ttl: banana\nproviders:\n  - name: chibigeo\n    rate_per_second: 1\n")
	_, err := Load(path, nil)
	must.Error(err)
	must.ErrorContains(err, "cache.ttl")
}

func TestUnknownKeyIsAnError(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, "cache:\n  max_entires: 500\nproviders:\n  - name: chibigeo\n    rate_per_second: 1\n")
	_, err := Load(path, nil)
	must.Error(err, "a typo'd key must fail startup, not be silently ignored")
	must.ErrorContains(err, "max_entires")
}

// Provider settings are handed on undecoded (env values stay strings); the
// provider's own strict decode is what rejects a bad number. That path is
// covered by TestBuildProviders in the serve package.
func TestProviderSettingsPassThroughUndecoded(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	t.Setenv("GEOCODER_PROVIDERS", "chibigeo")
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_RATE_PER_SECOND", "1.5")

	cfg, err := Load("", nil)
	must.NoError(err)
	must.Len(cfg.Providers, 1)
	is.Equal("1.5", cfg.Providers[0].Settings["rate_per_second"], "env values are strings until the provider decodes them")
}
