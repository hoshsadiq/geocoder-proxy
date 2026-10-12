package all_test

import (
	"testing"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
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
	must.Equal([]string{"chibigeo", "geoapify", "locationiq", "nominatim"}, registry.Names())

	for name, keys := range providerKeys {
		reg, ok := registry.Lookup(name)
		must.True(ok, "provider %q not registered", name)

		cfg := reg.NewConfig()
		k := koanf.New(".")
		must.NoError(k.Load(confmap.Provider(keys, "."), nil))
		must.NoError(k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
			DecoderConfig: &mapstructure.DecoderConfig{
				ErrorUnused:      true,
				Result:           cfg,
				WeaklyTypedInput: true,
			},
		}), "provider %q: its documented keys must decode strictly", name)
		must.NoError(cfg.Validate(), "provider %q: a complete config must validate", name)

		client, err := reg.Build(cfg)
		must.NoError(err, "provider %q build", name)
		is.Equal(name, client.Name(), "provider %q reports a different Name()", name)
	}
}

// Every registered provider must reject an invalid rate and an invalid
// base URL: the shared rules are opt-in per provider, so this is the guard
// that a new provider cannot silently skip them.
func TestSharedRulesEnforced(t *testing.T) {
	must := require.New(t)
	for _, name := range registry.Names() {
		reg, _ := registry.Lookup(name)
		cfg := reg.NewConfig()
		decode := func(m map[string]any) {
			k := koanf.New(".")
			must.NoError(k.Load(confmap.Provider(m, "."), nil))
			_ = k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
				DecoderConfig: &mapstructure.DecoderConfig{
					Result:           cfg,
					WeaklyTypedInput: true,
				},
			})
		}

		decode(map[string]any{"rate_per_second": "0"})
		must.Error(cfg.Validate(), "provider %q must reject a zero rate", name)

		decode(map[string]any{"rate_per_second": "1", "base_url": "not a url"})
		must.Error(cfg.Validate(), "provider %q must reject an invalid base_url", name)
	}
}
