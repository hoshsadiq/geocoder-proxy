package photon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language/display"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

type fakeService struct {
	reverseFn func(context.Context, domain.ReverseQuery) ([]domain.Result, error)
	forwardFn func(context.Context, domain.ForwardQuery) ([]domain.Result, error)
}

func (f *fakeService) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	return f.reverseFn(ctx, q)
}

func (f *fakeService) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	return f.forwardFn(ctx, q)
}

func mustCoords(t *testing.T, lat, lon float64) domain.Coordinates {
	t.Helper()
	c, err := domain.NewCoordinates(lat, lon)
	require.NoError(t, err)
	return c
}

func london(t *testing.T) domain.Result {
	t.Helper()
	return domain.Result{
		Coordinates: mustCoords(t, 51.4774, -0.061),
		Address: domain.Address{
			Name:        "10 Downing Street",
			HouseNumber: "10",
			Street:      "Downing Street",
			Postcode:    "SW1A 2AA",
			City:        "London",
			Country:     "Vereinigtes Königreich", // a localised name from a provider
			CountryCode: "GB",
			OsmID:       42,
			OsmType:     "W",
			OsmKey:      "building",
			OsmValue:    "office",
		},
	}
}

func TestReverseReturnsPhotonShape(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	svc := &fakeService{
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return []domain.Result{london(t)}, nil
		},
	}
	h := New(svc, display.English.Regions(), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/reverse?lat=51.4774&lon=-0.061&limit=1", nil))

	must.Equal(http.StatusOK, rec.Code)
	is.Equal("application/json", rec.Header().Get("Content-Type"))

	var body struct {
		Type     string `json:"type"`
		Features []struct {
			Properties struct {
				Name        string `json:"name"`
				Street      string `json:"street"`
				HouseNumber string `json:"housenumber"`
				City        string `json:"city"`
				Country     string `json:"country"`
				CountryCode string `json:"countrycode"`
				OsmID       int64  `json:"osm_id"`
				OsmType     string `json:"osm_type"`
				OsmKey      string `json:"osm_key"`
				OsmValue    string `json:"osm_value"`
			} `json:"properties"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	must.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	is.Equal("FeatureCollection", body.Type)
	must.Len(body.Features, 1)

	p := body.Features[0].Properties
	is.Equal("10 Downing Street", p.Name)
	is.Equal("Downing Street", p.Street)
	is.Equal("10", p.HouseNumber)
	is.Equal("London", p.City)
	is.Equal("United Kingdom", p.Country, "the country name is canonicalised from the code, not the provider's localised name")
	is.Equal("GB", p.CountryCode)
	is.Equal(int64(42), p.OsmID)
	is.Equal("W", p.OsmType)
	is.Equal("building", p.OsmKey)
	is.Equal("office", p.OsmValue)
	is.Equal([]float64{-0.061, 51.4774}, body.Features[0].Geometry.Coordinates, "GeoJSON is [lon, lat]")
}

func TestReverseInvalidCoordinatesAre400(t *testing.T) {
	must := require.New(t)
	h := New(&fakeService{}, nil, nil)

	for _, path := range []string{
		"/reverse?lat=banana&lon=0",
		"/reverse?lat=51.4",
		"/reverse?lat=NaN&lon=0",
		"/reverse?lat=91&lon=0",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		must.Equal(http.StatusBadRequest, rec.Code, path)
	}
}

func TestProviderFailureIs503(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	svc := &fakeService{
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return nil, domain.ErrAllProvidersFailed
		},
	}
	h := New(svc, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/reverse?lat=51.4&lon=-0.06", nil))

	// The geocoder gem Dawarich uses raises 503 as transient and retries;
	// a 502 would parse as an empty answer and be marked final.
	must.Equal(http.StatusServiceUnavailable, rec.Code)
	is.NotContains(rec.Body.String(), "51.4", "error bodies never carry coordinates")
}

func TestGenuineEmptyIsEmptyFeatureCollection(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	svc := &fakeService{
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return nil, nil
		},
	}
	h := New(svc, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/reverse?lat=0&lon=-30", nil))

	must.Equal(http.StatusOK, rec.Code)
	var body struct {
		Type     string `json:"type"`
		Features []any  `json:"features"`
	}
	must.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	is.Equal("FeatureCollection", body.Type)
	is.Empty(body.Features)
}

func TestForwardMissingQueryIs400(t *testing.T) {
	must := require.New(t)
	h := New(&fakeService{}, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api", nil))
	must.Equal(http.StatusBadRequest, rec.Code)
}

func TestForwardPassesLimit(t *testing.T) {
	is := assert.New(t)
	var gotLimit int
	svc := &fakeService{
		forwardFn: func(_ context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
			gotLimit = q.Limit
			return nil, nil
		},
	}
	h := New(svc, nil, nil)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api?q=main+street&limit=5", nil))
	is.Equal(5, gotLimit)
}
