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

// The fixtures follow LocationIQ's documented example responses (their
// docs/OpenAPI examples), including the quoted numeric fields LocationIQ
// actually sends: "osm_id": "5568763423", not an unquoted number.
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
		Coordinates: mustCoords(t, 51.5234, -0.1582),
	})
	must.NoError(err)
	must.Len(got, 1)

	a := got[0].Address
	is.Equal("Sherlock Holmes Museum", a.Name)
	is.Equal("museum", a.Type)
	is.Equal("221B", a.HouseNumber)
	is.Equal("Baker Street", a.Street)
	is.Equal("NW1 6XE", a.Postcode)
	is.Equal("London", a.City)
	is.Equal("Marylebone", a.District)
	is.Equal("Greater London", a.County, "state_district fills County when county is absent")
	is.Equal("England", a.State)
	is.Equal("United Kingdom", a.Country)
	is.Equal("GB", a.CountryCode, "lowercase country codes are normalised to uppercase")
	is.Equal("N", a.OsmType, "word-form osm types are normalised to letters")
	is.Equal(int64(5568763423), a.OsmID, "LocationIQ sends osm_id as a JSON string")
	is.InDelta(51.5233879, got[0].Coordinates.Lat(), 0.00001)
}

func TestForwardParsesLocationIQResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("/v1/search", r.URL.Path)
		is.Equal("empire state building", r.URL.Query().Get("q"))
		is.Equal("2", r.URL.Query().Get("limit"))
		is.Equal("1", r.URL.Query().Get("addressdetails"), "without it the search endpoint returns no address object")
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{
		Text:  "empire state building",
		Limit: 2,
	})
	must.NoError(err)
	must.Len(got, 1)
	a := got[0].Address
	is.Equal("Empire State Building", a.Name)
	is.Equal("tourism", a.OsmKey, "LocationIQ's class maps to OsmKey")
	is.Equal("attraction", a.OsmValue, "LocationIQ's type maps to OsmValue")
	is.Equal("Manhattan Community Board 5", a.District)
}

func TestNoMatchErrorBodyIsGenuineEmpty(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"Unable to geocode"}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 0, -30),
	})
	must.NoError(err)
	must.Empty(got, "the documented no-match marker is a genuine nothing-here")
}

func TestOtherErrorBodyIsAnError(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"Rate Limited Day"}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 0, -30),
	})
	must.Error(err, "an error body that is not the no-match marker is a failure, never a cacheable empty")
	is.Nil(got)
	is.NotContains(err.Error(), "0,-30")
}

func TestForwardErrorBodyClassification(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	for body, want := range map[string]struct{ empty, isErr bool }{
		`{"error":"Unable to geocode"}`: {true, false},
		`{"error":"Rate Limited Day"}`:  {false, true},
		`{"error":""}`:                  {false, true},
		`<html>not json</html>`:         {false, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{Text: "x"})
		srv.Close()
		if want.isErr {
			must.Error(err, "body %s", body)
			is.Nil(got)
		} else if want.empty {
			must.NoError(err, "body %s", body)
			is.Empty(got, "the no-match marker is a genuine empty on forward too")
		}
	}
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
