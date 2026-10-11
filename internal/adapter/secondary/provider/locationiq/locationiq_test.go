package locationiq

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// The fixtures follow LocationIQ's documented example response (their
// OpenAPI spec, Empire State Building): the shape is Nominatim-compatible,
// with lat/lon as strings and a lowercase country code.
func serve(t *testing.T, fixture string, handlerCheck func(*http.Request)) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(fixture)
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handlerCheck != nil {
			handlerCheck(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(registry.Config{BaseURL: base, APIKey: "test-key"})
	require.NoError(t, err)
	return c
}

func TestReverseParsesLocationIQResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("/v1/reverse", r.URL.Path)
		is.Equal("json", r.URL.Query().Get("format"))
		is.Equal("test-key", r.URL.Query().Get("key"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 40.7484, -73.9856),
	})
	must.NoError(err)
	must.Len(got, 1)

	a := got[0].Address
	is.Equal("Empire State Building", a.Name)
	is.Equal("building", a.Type)
	is.Equal("350", a.HouseNumber)
	is.Equal("5th Avenue", a.Street)
	is.Equal("10001", a.Postcode)
	is.Equal("New York", a.City)
	is.Equal("Manhattan Community Board 5", a.District)
	is.Equal("New York County", a.County)
	is.Equal("New York", a.State)
	is.Equal("United States", a.Country)
	is.Equal("US", a.CountryCode, "lowercase country codes are normalised to uppercase")
	is.Equal("W", a.OsmType, "word-form osm types are normalised to letters")
	is.Equal(int64(34633854), a.OsmID)
	is.InDelta(40.74844205, got[0].Coordinates.Lat(), 0.00001)
}

func TestForwardParsesLocationIQResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("/v1/search", r.URL.Path)
		is.Equal("empire state building", r.URL.Query().Get("q"))
		is.Equal("2", r.URL.Query().Get("limit"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{
		Text:  "empire state building",
		Limit: 2,
	})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("Empire State Building", got[0].Address.Name)
}

func TestAPIKeyRequired(t *testing.T) {
	must := require.New(t)
	_, err := New(registry.Config{})
	must.Error(err)
	must.ErrorContains(err, "APIKey")
}

func TestUpstreamErrorIsCoordinateFree(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 40.7484, -73.9856),
	})
	must.Error(err)
	is.Contains(err.Error(), "429")
	is.NotContains(err.Error(), "40.7484")
	is.NotContains(err.Error(), srv.URL)
	is.NotContains(err.Error(), "test-key", "provider errors must never carry the API key")
}

func mustCoords(t *testing.T, lat, lon float64) domain.Coordinates {
	t.Helper()
	c, err := domain.NewCoordinates(lat, lon)
	require.NoError(t, err)
	return c
}
