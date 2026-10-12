package all_test

import (
	"testing"

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
		factory, ok := registry.Lookup(name)
		must.True(ok, "provider %q not registered", name)
		client, err := factory(registry.Config{
			BaseURL:   "http://127.0.0.1",
			APIKey:    "test",
			UserAgent: "test",
		})
		must.NoError(err, "provider %q factory", name)
		assert.Equal(t, name, client.Name(), "provider %q reports a different Name()", name)
	}
}
