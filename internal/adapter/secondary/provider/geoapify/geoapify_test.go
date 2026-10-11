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

// reverse.json is a live capture from the Geoapify reverse endpoint with
// format=json (Boston City Hall, 2026-10-11). forward.json is the
// documented forward example (1313 Broadway, Tacoma). Their Geocoding API
// returns a flat `results` array with format=json, not the default GeoJSON.
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
		is.Equal("json", r.URL.Query().Get("format"), "the API defaults to GeoJSON without it")
		is.Equal("test-key", r.URL.Query().Get("apiKey"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.NoError(err)
	must.Len(got, 1)

	a := got[0].Address
	is.Equal("1", a.HouseNumber)
	is.Equal("City Hall Square", a.Street)
	is.Equal("02201", a.Postcode)
	is.Equal("Boston", a.City)
	is.Equal("Boston", a.District)
	is.Equal("Suffolk County", a.County)
	is.Equal("Massachusetts", a.State)
	is.Equal("United States", a.Country)
	is.Equal("US", a.CountryCode, "lowercase country codes are normalised to uppercase")
	is.Zero(a.OsmID, "Geoapify geocoding returns no OSM identifiers")
	is.InDelta(42.3601, got[0].Coordinates.Lat(), 0.01)
}

func TestForwardSendsTextParam(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("/v1/geocode/search", r.URL.Path)
		is.Equal("main street", r.URL.Query().Get("text"))
		is.Equal("5", r.URL.Query().Get("limit"))
		is.Equal("json", r.URL.Query().Get("format"), "without it the API answers GeoJSON, which would read as a genuine empty")
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 5})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("1313 Broadway", got[0].Address.HouseNumber+" "+got[0].Address.Street)
}

func TestErrorObjectIsAnError(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"statusCode":401,"error":"Unauthorized","message":"Invalid apiKey"}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.Error(err, "a 200 error object is a failure, never a cacheable empty")
	is.Nil(got)
}

func TestEntryWithoutCoordinatesIsDropped(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"name":"No Coords Here","lat":null,"lon":null},{"name":"Somewhere","lat":51.5,"lon":-0.06}]}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.5, -0.06),
	})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("Somewhere", got[0].Address.Name)
	is.InDelta(51.5, got[0].Coordinates.Lat(), 0.0001)
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
