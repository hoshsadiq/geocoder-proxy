package nominatim

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

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(&Config{BaseURL: base, UserAgent: "geocoder-proxy-test", RatePerSecond: 1})
	require.NoError(t, err)
	return c
}

// The fixtures are real responses captured from nominatim.openstreetmap.org
// (2026-10-11).
func TestReverseParsesNominatimResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("/reverse", r.URL.Path)
		is.Equal("jsonv2", r.URL.Query().Get("format"))
		is.Equal("1", r.URL.Query().Get("addressdetails"))
		is.Equal("geocoder-proxy-test", r.Header.Get("User-Agent"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.NoError(err)
	must.Len(got, 1)

	a := got[0].Address
	is.Equal("Studholme Street", a.Name)
	is.Equal("Studholme Street", a.Street)
	is.Equal("road", a.Type)
	is.Equal("SE15 1DD", a.Postcode)
	is.Equal("Greater London", a.City)
	is.Equal("London Borough of Southwark", a.District, "city_district wins over suburb")
	is.Equal("England", a.State)
	is.Equal("United Kingdom", a.Country)
	is.Equal("GB", a.CountryCode, "lowercase country codes are normalised to uppercase")
	is.Equal("W", a.OsmType, "word-form osm types are normalised to letters")
	is.Equal(int64(11550063), a.OsmID)
	is.InDelta(51.4776748, got[0].Coordinates.Lat(), 0.00001)
}

func TestForwardParsesNominatimResponse(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/forward.json", func(r *http.Request) {
		is.Equal("/search", r.URL.Path)
		is.Equal("main street", r.URL.Query().Get("q"))
		is.Equal("2", r.URL.Query().Get("limit"))
	})
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 2})
	must.NoError(err)
	must.Len(got, 2)
	is.Equal("315", got[0].Address.HouseNumber)
	is.Equal("Toronto", got[0].Address.City)
	is.Equal("CA", got[0].Address.CountryCode)
}

func TestNoMatchErrorBodyIsGenuineEmpty(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"Unable to geocode","place_id":0}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 0, -30),
	})
	must.NoError(err)
	must.Empty(got, "Nominatim's documented no-match marker is a genuine nothing-here")
}

func TestOtherErrorBodyIsAnError(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"Bandwidth limit exceeded"}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 0, -30),
	})
	must.Error(err, "an error body that is not the no-match marker is a failure, never a cacheable empty")
	is.Nil(got)
}

func TestAPIKeySentWhenConfigured(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := serve(t, "testdata/reverse.json", func(r *http.Request) {
		is.Equal("hosted-key", r.URL.Query().Get("key"))
	})
	defer srv.Close()

	c, err := New(&Config{BaseURL: srv.URL, UserAgent: "ua", APIKey: "hosted-key", RatePerSecond: 1})
	must.NoError(err)
	_, err = c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.NoError(err)
}

func TestOsmKeyValueAndCountyFallback(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"osm_id":11550063,"osm_type":"way","lat":"51.4776748","lon":"-0.0610621",
			"category":"highway","type":"residential","addresstype":"road","name":"Studholme Street",
			"address":{"road":"Studholme Street","city":"London","state_district":"Greater London",
			"country":"United Kingdom","country_code":"gb"}}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("highway", got[0].Address.OsmKey, "Nominatim's category maps to OsmKey")
	is.Equal("residential", got[0].Address.OsmValue, "Nominatim's type maps to OsmValue")
	is.Equal("Greater London", got[0].Address.County, "state_district fills County when county is absent")
}

func TestForwardErrorBodyClassification(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	for body, want := range map[string]struct{ empty, isErr bool }{
		`{"error":"Unable to geocode"}`:        {true, false},
		`{"error":"Bandwidth limit exceeded"}`: {false, true},
		`{"error":""}`:                         {false, true},
		`<html>not json</html>`:                {false, true},
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

func TestUserAgentRequired(t *testing.T) {
	must := require.New(t)
	_, err := New(&Config{})
	must.Error(err)
	must.ErrorContains(err, "user_agent")
}

func TestUpstreamErrorIsCoordinateFree(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Reverse(t.Context(), domain.ReverseQuery{
		Coordinates: mustCoords(t, 51.4774, -0.061),
	})
	must.Error(err)
	is.NotContains(err.Error(), "51.4774")
	is.NotContains(err.Error(), srv.URL)
}

func mustCoords(t *testing.T, lat, lon float64) domain.Coordinates {
	t.Helper()
	c, err := domain.NewCoordinates(lat, lon)
	require.NoError(t, err)
	return c
}
