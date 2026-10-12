package photon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

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

func newTestClient(t *testing.T, base, key string) *Client {
	t.Helper()
	c, err := New(&Config{BaseURL: base, APIKey: key, RatePerSecond: 1})
	require.NoError(t, err)
	return c
}

// The fixtures are real responses captured from photon.komoot.io
// (2026-10-11), which is the same wire shape ChibiGeo serves.
func TestReverseParsesPhotonResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("/reverse", r.URL.Path)
		is.Equal("51.4774", r.URL.Query().Get("lat"))
		is.Equal("-0.061", r.URL.Query().Get("lon"))
		is.Equal("3", r.URL.Query().Get("limit"))
		is.Equal("1", r.URL.Query().Get("radius"))
		is.Equal("true", r.URL.Query().Get("distance_sort"))
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	got, err := c.Reverse(t.Context(), domain.ReverseQuery{
		Coordinates:  mustCoords(t, 51.4774, -0.061),
		Limit:        3,
		Radius:       1,
		DistanceSort: true,
	})
	must.NoError(err)
	must.Len(got, 3)

	first := got[0]
	is.Equal("SE15 1QH", first.Address.Name)
	is.Equal("London", first.Address.City)
	is.Equal("London Borough of Southwark", first.Address.District)
	is.Equal("United Kingdom", first.Address.Country)
	is.Equal("GB", first.Address.CountryCode)
	is.Equal("place", first.Address.OsmKey)
	is.Equal("postcode", first.Address.OsmValue)
	is.InDelta(-0.06103654, first.Coordinates.Lon(), 0.00001)
}

func TestForwardParsesPhotonResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("/api", r.URL.Path)
		is.Equal("main street", r.URL.Query().Get("q"))
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	got, err := c.Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 2})
	must.NoError(err)
	must.Len(got, 2)
	is.Equal("Main Street", got[0].Address.Name)
	is.Equal("Main Bus Terminal", got[0].Address.Street)
	is.Equal("CA", got[0].Address.CountryCode)
	is.Equal("N", got[0].Address.OsmType)
}

func TestAPIKeyHeaderSent(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("secret-key", r.Header.Get("X-Api-Key"))
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, "secret-key")
	_, err := c.Forward(t.Context(), domain.ForwardQuery{Text: "x"})
	must.NoError(err)
}

func TestUpstreamErrorIsCoordinateFree(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.Error(err)
	is.Contains(err.Error(), "429")
	is.NotContains(err.Error(), "51.4774", "provider errors must never carry coordinates")
	is.NotContains(err.Error(), srv.URL, "provider errors must never carry the request URL")
}

func TestEmptyFeatureCollectionIsGenuineEmpty(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	got, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.NoError(err)
	must.Empty(got)
}

func TestFeaturesWithInvalidCoordinatesAreDropped(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[
			{"type":"Feature","properties":{"name":"Bad Coords"},"geometry":{"type":"Point","coordinates":[999,999]}},
			{"type":"Feature","properties":{"name":"No Geometry"}},
			{"type":"Feature","properties":{"name":"Good","countrycode":"gb"},"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	got, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("Good", got[0].Address.Name)
	is.Equal("GB", got[0].Address.CountryCode, "countrycode is normalised to uppercase")
}

func TestOsmIDAsQuotedStringDecodes(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[
			{"type":"Feature","properties":{"osm_id":"10033986098","osm_type":"N","name":"Quoted ID"},
			 "geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}
		]}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL, "").Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(int64(10033986098), got[0].Address.OsmID, "some Photon-shaped upstreams quote the osm id")
}

func mustCoords(t *testing.T, lat, lon float64) domain.Coordinates {
	t.Helper()
	c, err := domain.NewCoordinates(lat, lon)
	require.NoError(t, err)
	return c
}
