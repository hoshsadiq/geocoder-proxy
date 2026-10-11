package all_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"

	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/all"
)

// Importing all must register every provider.
func TestAllProvidersRegistered(t *testing.T) {
	assert.Equal(t, []string{"chibigeo", "geoapify", "locationiq", "nominatim"}, registry.Names())
}
