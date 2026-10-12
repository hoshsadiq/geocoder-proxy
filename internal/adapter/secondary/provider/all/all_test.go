package all_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"

	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/all"
)

// providerKeys is each provider's full operator-facing key set, mirroring
// how the config layer feeds the registry: koanf decode, unknown keys
// rejected. Values are strings on purpose: the env layer produces strings,
// and weak typing must carry them into the typed fields.
var providerKeys = map[string]map[string]any{
	"chibigeo": {
		"base_url":        "http://127.0.0.1",
		"api_key":         "test",
		"rate_per_second": "1",
		"daily_quota":     "2500",
	},
	"geoapify": {
		"base_url":        "http://127.0.0.1",
		"api_key":         "test",
		"rate_per_second": "5",
		"daily_quota":     "3000",
	},
	"locationiq": {
		"base_url":        "http://127.0.0.1",
		"api_key":         "test",
		"rate_per_second": "2",
		"daily_quota":     "5000",
	},
	"nominatim": {
		"base_url":        "http://127.0.0.1",
		"api_key":         "test",
		"user_agent":      "test-agent",
		"rate_per_second": "1",
	},
}

// Importing all must register every provider, and each registered client
// must build from its own strict decode: the operator-facing keys, the
// validation, the client identity, and the router limits all round-trip.
func TestAllProvidersRegistered(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	must.Equal(slices.Sorted(maps.Keys(providerKeys)), registry.Names())

	for name, keys := range providerKeys {
		reg, ok := registry.Lookup(name)
		must.True(ok, "provider %q not registered", name)

		cfg := decode(t, reg.NewConfig(), keys)
		must.NoError(cfg.Validate(), "provider %q: a complete config must validate", name)

		client, err := reg.Build(cfg)
		must.NoError(err, "provider %q build", name)
		is.Equal(name, client.Name(), "provider %q reports a different Name()", name)
	}
}

// Every registered provider must reject an invalid rate and an invalid
// base URL: the shared rules are opt-in per provider, so this is the guard
// that a new provider cannot silently skip them. Each case decodes a
// complete valid config and then breaks exactly one key, so the error can
// only come from the rule under test.
func TestSharedRulesEnforced(t *testing.T) {
	must := require.New(t)
	for name, keys := range providerKeys {
		reg, _ := registry.Lookup(name)

		badRate := maps.Clone(keys)
		badRate["rate_per_second"] = "0"
		must.Error(decode(t, reg.NewConfig(), badRate).Validate(),
			"provider %q must reject a zero rate", name)

		badURL := maps.Clone(keys)
		badURL["base_url"] = "not a url"
		must.Error(decode(t, reg.NewConfig(), badURL).Validate(),
			"provider %q must reject an invalid base_url", name)
	}
}

// decode mirrors the config layer's decode through the real registry
// implementation, so the hook it uses is the one under test.
func decode(t *testing.T, cfg registry.Config, m map[string]any) registry.Config {
	t.Helper()
	require.NoError(t, registry.Decode(cfg, m))
	return cfg
}
