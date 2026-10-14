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

// The same provider type can appear more than once, as separate instances with
// their own name, tier and settings. The instance keys do not reach the
// provider's own decode.
func TestLoadMultipleInstancesOfOneType(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
providers:
  - name: chibi-eu
    type: chibigeo
    base_url: https://eu.example
    rate_per_second: 1
    daily_quota: 2500
  - name: chibi-us
    type: chibigeo
    base_url: https://us.example
    rate_per_second: 1
    daily_quota: 2500
    tier: 2
`)

	cfg, err := Load(path, nil)
	must.NoError(err)
	must.Len(cfg.Providers, 2)
	is.Equal("chibi-eu", cfg.Providers[0].Name)
	is.Equal("chibigeo", cfg.Providers[0].Type)
	is.Equal(1, cfg.Providers[0].Tier)
	is.Equal("https://eu.example", cfg.Providers[0].Settings["base_url"])
	is.Equal("chibigeo", cfg.Providers[1].Type)
	is.Equal(2, cfg.Providers[1].Tier)
	is.Equal("https://us.example", cfg.Providers[1].Settings["base_url"])
	is.NotContains(cfg.Providers[0].Settings, "type")
	is.NotContains(cfg.Providers[0].Settings, "tier")
}

// A single entry with no type or tier keeps working: the type defaults to the
// instance name and the tier gives it a place of its own.
func TestProviderTypeAndTierDefaultToSingleEntryShape(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, "providers:\n  - name: chibigeo\n    rate_per_second: 1\n")

	cfg, err := Load(path, nil)
	must.NoError(err)
	must.Len(cfg.Providers, 1)
	is.Equal("chibigeo", cfg.Providers[0].Type)
	is.Equal(1, cfg.Providers[0].Tier)
}

// The tier is settable from the environment too, like every other instance key.
func TestTierFromEnv(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, "providers:\n  - name: rescue\n    type: chibigeo\n    rate_per_second: 1\n")
	t.Setenv("GEOCODER_PROVIDER_RESCUE_TIER", "3")

	cfg, err := Load(path, nil)
	must.NoError(err)
	is.Equal(3, cfg.Providers[0].Tier)
}

func TestLoadRejectsMalformedTier(t *testing.T) {
	for _, tier := range []string{"0", "-1", "1.5", "banana"} {
		t.Run(tier, func(t *testing.T) {
			must := require.New(t)
			path := writeYAML(t, "providers:\n  - name: chibigeo\n    rate_per_second: 1\n    tier: "+tier+"\n")
			_, err := Load(path, nil)
			must.Error(err)
			must.ErrorContains(err, "tier must be a positive integer")
		})
	}
}

// A positive number above the bound is not a "positive integer" problem, so it
// gets its own message naming the bound. Go saturates a float-to-int
// conversion, so 1e300 would otherwise arrive as the largest tier.
func TestLoadRejectsOutOfRangeTier(t *testing.T) {
	tests := []struct {
		tier string
		want string
	}{
		{tier: ".inf", want: "tier must be at most 1000000"},
		{tier: "1e300", want: "tier must be at most 1000000"},
		{tier: "1000001", want: "tier must be at most 1000000"},
		{tier: "-.inf", want: "tier must be a positive integer"},
		{tier: ".nan", want: "tier must be a positive integer"},
	}
	for _, tt := range tests {
		t.Run(tt.tier, func(t *testing.T) {
			must := require.New(t)
			path := writeYAML(t, "providers:\n  - name: chibigeo\n    rate_per_second: 1\n    tier: "+tt.tier+"\n")
			_, err := Load(path, nil)
			must.Error(err)
			must.ErrorContains(err, tt.want)
		})
	}
}

// A type that is present but not a non-empty string is rejected, not silently
// replaced by the instance name.
func TestLoadRejectsNonStringType(t *testing.T) {
	for _, value := range []string{"5", "[x]", "true", "null", "''"} {
		t.Run(value, func(t *testing.T) {
			must := require.New(t)
			path := writeYAML(t, "providers:\n  - name: chibigeo\n    type: "+value+"\n    rate_per_second: 1\n")
			_, err := Load(path, nil)
			must.Error(err)
			must.ErrorContains(err, "type must be a non-empty string")
		})
	}
}

// With no tier written, each provider gets a tier of its own in list order, so
// a two-provider config keeps the strict order it had before tiers existed.
func TestProvidersWithoutTierKeepConfigOrder(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
providers:
  - name: chibigeo
    rate_per_second: 1
  - name: nominatim
    user_agent: x
    rate_per_second: 1
`)

	cfg, err := Load(path, nil)
	must.NoError(err)
	must.Len(cfg.Providers, 2)
	is.Equal(1, cfg.Providers[0].Tier)
	is.Equal(2, cfg.Providers[1].Tier, "the second provider is its own tier, after the first")
}

// A tier the loader assigns by position must not silently take the number
// another entry set explicitly: the two would rotate, and the one written
// second would stop being a fallback.
func TestLoadRejectsTierCollision(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, `
providers:
  - name: first
    rate_per_second: 1
  - name: second
    rate_per_second: 1
  - name: third
    rate_per_second: 1
    tier: 2
`)

	_, err := Load(path, nil)
	must.Error(err)
	must.ErrorContains(err, `provider "second" has no tier`)
	must.ErrorContains(err, "tier it takes (2)")
	must.ErrorContains(err, `provider "third"`)
}

// The mixed shape still works when the written tiers do not land on a
// provider's own position: two providers rotate, the untiered one keeps its
// turn after them.
func TestLoadAllowsMixedExplicitAndImplicitTiers(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
providers:
  - name: a
    rate_per_second: 1
    tier: 1
  - name: b
    rate_per_second: 1
    tier: 1
  - name: c
    rate_per_second: 1
`)

	cfg, err := Load(path, nil)
	must.NoError(err)
	must.Len(cfg.Providers, 3)
	is.Equal(1, cfg.Providers[0].Tier)
	is.Equal(1, cfg.Providers[1].Tier)
	is.Equal(3, cfg.Providers[2].Tier, "the untiered provider keeps its own turn after the rotation")
}

func TestLoadRejectsDuplicateInstanceName(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, `
providers:
  - name: chibi-eu
    type: chibigeo
    rate_per_second: 1
  - name: chibi-eu
    type: geoapify
    api_key: x
    rate_per_second: 1
`)
	_, err := Load(path, nil)
	must.Error(err, "two instances must not share a name")
	must.ErrorContains(err, "more than once")
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

func TestLoadTimeouts(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, `
request_timeout: 6s
upstream_timeout: 2500ms
providers:
  - name: chibigeo
    rate_per_second: 1
`)

	cfg, err := Load(path, nil)
	must.NoError(err)
	is.Equal(6*time.Second, cfg.RequestTimeout)
	is.Equal(2500*time.Millisecond, cfg.UpstreamTimeout)

	t.Setenv("GEOCODER_REQUEST_TIMEOUT", "9s")
	cfg, err = Load(path, nil)
	must.NoError(err)
	is.Equal(9*time.Second, cfg.RequestTimeout, "env beats the file")
}

// The example file is the shape an operator copies; its timeout values must
// match the exported defaults the code ships, or the docs drift silently.
func TestExampleConfigMatchesDefaults(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cfg, err := Load(filepath.Join("..", "..", "..", "config.example.yaml"), nil)
	must.NoError(err, "the shipped example must load and validate")
	is.Equal(DefaultRequestTimeout, cfg.RequestTimeout)
	is.Equal(DefaultUpstreamTimeout, cfg.UpstreamTimeout)
}

func TestDefaultTimeouts(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	path := writeYAML(t, "providers:\n  - name: chibigeo\n    rate_per_second: 1\n")
	cfg, err := Load(path, nil)
	must.NoError(err)
	is.Equal(DefaultRequestTimeout, cfg.RequestTimeout)
	is.Equal(DefaultUpstreamTimeout, cfg.UpstreamTimeout)
}

func TestInvalidRequestTimeoutError(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, "request_timeout: banana\nproviders:\n  - name: chibigeo\n    rate_per_second: 1\n")
	_, err := Load(path, nil)
	must.Error(err)
	must.ErrorContains(err, "request_timeout")
}

func TestUpstreamTimeoutAboveHalfBudgetError(t *testing.T) {
	must := require.New(t)
	// Positive and below the budget, but leaving no room for a pace wait plus a
	// failover: rejected.
	path := writeYAML(t, "request_timeout: 4s\nupstream_timeout: 3s\nproviders:\n  - name: chibigeo\n    rate_per_second: 1\n")
	_, err := Load(path, nil)
	must.Error(err)
	must.ErrorContains(err, "at most half")
}

func TestUpstreamTimeoutAboveBudgetError(t *testing.T) {
	must := require.New(t)
	path := writeYAML(t, "request_timeout: 2s\nupstream_timeout: 5s\nproviders:\n  - name: chibigeo\n    rate_per_second: 1\n")
	_, err := Load(path, nil)
	must.Error(err)
	must.ErrorContains(err, "at most half")
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

func TestUnknownProviderEnvNameIsAnError(t *testing.T) {
	must := require.New(t)
	t.Setenv("GEOCODER_PROVIDERS", "chibigeo")
	t.Setenv("GEOCODER_PROVIDER_CHIBIGEO_RATE_PER_SECOND", "1")
	t.Setenv("GEOCODER_PROVIDER_CHIBI_GEO_RATE_PER_SECOND", "10")

	_, err := Load("", nil)
	must.Error(err, "an env var naming no configured provider must not be silently ignored")
	must.ErrorContains(err, "CHIBI_GEO")
}
