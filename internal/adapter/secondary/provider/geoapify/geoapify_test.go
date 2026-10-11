package geoapify

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

// The fixture is the documented example response from Geoapify's Geocoding
// API docs (apidocs.geoapify.com/docs/geocoding/, 2026-10-11): their
// Geocoding API returns a flat `results` array, not GeoJSON.
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

func TestReverseParsesGeoapifyResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("/v1/geocode/reverse", r.URL.Path)
		is.Equal("51.4774", r.URL.Query().Get("lat"))
		is.Equal("test-key", r.URL.Query().Get("apiKey"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.NoError(err)
	must.Len(got, 1)

	a := got[0].Address
	is.Equal("1313", a.HouseNumber)
	is.Equal("Broadway", a.Street)
	is.Equal("building", a.Type)
	is.Equal("98402", a.Postcode)
	is.Equal("Tacoma", a.City)
	is.Equal("G Street Neighborhood", a.District, "suburb fills district")
	is.Equal("Pierce County", a.County)
	is.Equal("Washington", a.State)
	is.Equal("United States of America", a.Country)
	is.Equal("US", a.CountryCode, "lowercase country codes are normalised to uppercase")
	is.Zero(a.OsmID, "Geoapify geocoding returns no OSM identifiers")
	is.InDelta(47.25094496732003, got[0].Coordinates.Lat(), 0.00001)
}

func TestForwardSendsTextParam(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("/v1/geocode/search", r.URL.Path)
		is.Equal("main street", r.URL.Query().Get("text"))
		is.Equal("5", r.URL.Query().Get("limit"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 5})
	must.NoError(err)
	must.Len(got, 1)
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
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.Error(err)
	is.Contains(err.Error(), "429")
	is.NotContains(err.Error(), "51.4774")
	is.NotContains(err.Error(), srv.URL)
	is.NotContains(err.Error(), "test-key", "provider errors must never carry the API key")
}

func mustCoords(t *testing.T, lat, lon float64) domain.Coordinates {
	t.Helper()
	c, err := domain.NewCoordinates(lat, lon)
	require.NoError(t, err)
	return c
}
