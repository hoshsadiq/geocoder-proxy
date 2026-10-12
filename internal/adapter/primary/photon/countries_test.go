package photon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

func TestDawarichCountryOverrides(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	// The table exists for codes where CLDR English disagrees with
	// Dawarich's seed names; it must stay exactly that set.
	must.Len(dawarichCountryNames, 28, "the override set changed; re-derive it from Dawarich's seed")
	for code, name := range dawarichCountryNames {
		is.NotEmpty(name, code)
		region, err := language.ParseRegion(code)
		must.NoError(err, code)
		cldr := display.English.Regions().Name(region)
		is.NotEqual(cldr, name, "%s: an override identical to CLDR is dead weight", code)
	}
	// Spot-check the divergences Dawarich actually hits.
	is.Equal("Myanmar", dawarichCountryNames["MM"])
	is.Equal("Hong Kong S.A.R.", dawarichCountryNames["HK"])
	is.Equal("North Macedonia", dawarichCountryNames["MK"])
}

func TestCountryCanonicalisationUsesOverride(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	svc := &fakeService{
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return []domain.Result{{
				Coordinates: mustCoords(t, 22.0, 114.1),
				Address:     domain.Address{Country: "Hong Kong", CountryCode: "HK"},
			}}, nil
		},
	}
	h := New(svc, display.English.Regions(), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/reverse?lat=22&lon=114.1", nil))
	must.Equal(http.StatusOK, rec.Code)

	var body struct {
		Features []struct {
			Properties struct {
				Country string `json:"country"`
			} `json:"properties"`
		} `json:"features"`
	}
	must.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	is.Equal("Hong Kong S.A.R.", body.Features[0].Properties.Country,
		"the response carries the name Dawarich resolves, not the CLDR name")
}

func TestUnknownErrorIs503(t *testing.T) {
	must := require.New(t)
	svc := &fakeService{
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return nil, errors.New("something nobody anticipated")
		},
	}
	h := New(svc, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/reverse?lat=51.4&lon=-0.06", nil))
	// The gem Dawarich pins raises only on 400/401/402/429/503; any other
	// status with a JSON body parses into a final empty answer.
	must.Equal(http.StatusServiceUnavailable, rec.Code)
}

func TestCancelledRequestWritesNothing(t *testing.T) {
	is := assert.New(t)
	svc := &fakeService{
		reverseFn: func(ctx context.Context, _ domain.ReverseQuery) ([]domain.Result, error) {
			return nil, ctx.Err()
		},
	}
	h := New(svc, nil, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/reverse?lat=51.4&lon=-0.06", nil))
	is.Equal(http.StatusOK, rec.Code, "the default recorder status; nothing was written")
	is.Empty(rec.Body.String())
}

func TestLimitIsClamped(t *testing.T) {
	is := assert.New(t)
	var gotLimit int
	svc := &fakeService{
		forwardFn: func(_ context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
			gotLimit = q.Limit
			return nil, nil
		},
	}
	h := New(svc, nil, nil)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api?q=x&limit=99999", nil))
	is.Equal(maxResultLimit, gotLimit)
}
