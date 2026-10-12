package all_test

import (
	"testing"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"

	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/all"
)

// Importing all must register every provider, and each registered client's
// Name() must equal its registry key, or config, metric labels and logs
// would disagree about which provider served a call.
func TestAllProvidersRegistered(t *testing.T) {
	must := require.New(t)
	want := []string{"chibigeo", "geoapify", "locationiq", "nominatim"}
	must.Equal(want, registry.Names())

	for _, name := range want {
		reg, ok := registry.Lookup(name)
		must.True(ok, "provider %q not registered", name)
		cfg := reg.New()
		// A union of every provider's fields; decoding is non-strict here
		// because each provider only reads its own keys.
		must.NoError(mapstructure.Decode(map[string]any{
			"base_url":        "http://127.0.0.1",
			"api_key":         "test",
			"user_agent":      "test",
			"rate_per_second": 1,
		}, cfg))
		must.NoError(cfg.Validate(), "provider %q config", name)
		client, err := reg.Build(cfg)
		must.NoError(err, "provider %q build", name)
		assert.Equal(t, name, client.Name(), "provider %q reports a different Name()", name)
	}
}
