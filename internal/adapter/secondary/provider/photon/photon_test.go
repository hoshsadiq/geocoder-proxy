package photon

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/internal/httpfetch"
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

// cappedUpstream answers the first request with a 429 carrying body, then a
// genuine empty Photon answer, and counts the requests. A terminal response
// leaves the count at one; anything retried reaches the second request and
// succeeds.
func cappedUpstream(t *testing.T, body string, retryAfter string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// TestRetryPolicyByResponseBody pins which 429 bodies end the call: ChibiGeo's
// cap codes are terminal (one request, then failover), while a plain rate limit
// or an unrecognised body takes the ordinary retry path.
func TestRetryPolicyByResponseBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCalls int32
		wantCode  string // "" means the retried second request answers 200
	}{
		{name: "plan cap is terminal", body: `{"error":"limit_exceeded"}`, wantCalls: 1, wantCode: "limit_exceeded"},
		{name: "quota exceeded is terminal", body: `{"error":"quota_exceeded"}`, wantCalls: 1, wantCode: "quota_exceeded"},
		{name: "past-due subscription is terminal", body: `{"error":"subscription_past_due"}`, wantCalls: 1, wantCode: "subscription_past_due"},
		{name: "rate limited is retried", body: `{"error":"rate_limited"}`, wantCalls: 2},
		{name: "unknown error code is retried", body: `{"error":"over_query_limit"}`, wantCalls: 2},
		{name: "non-JSON error body is retried", body: `<html>slow down</html>`, wantCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			srv, calls := cappedUpstream(t, tt.body, "")
			c := newTestClient(t, srv.URL, "")

			_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			if tt.wantCode == "" {
				must.NoError(err, "a non-terminal 429 is retried and the second attempt succeeds")
			} else {
				must.Error(err)
				is.Contains(err.Error(), "429")
				is.Contains(err.Error(), tt.wantCode)
				is.NotContains(err.Error(), "51.4774", "provider errors never carry coordinates")
				is.NotContains(err.Error(), srv.URL, "provider errors never carry the request URL")
			}
			is.Equal(tt.wantCalls, calls.Load(), "upstream requests for this provider")
		})
	}
}

// alwaysCapped answers every request with a 429 carrying body and counts them,
// so a test can assert the exact number of upstream requests a body shape makes.
func alwaysCapped(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// TestTerminalCapBodyLongerThanThePrefix pins that classification survives the
// httpfetch body cap (4KiB): the first JSON value is decoded, so trailing bytes
// past it do not matter. Each body must still make exactly one request.
func TestTerminalCapBodyLongerThanThePrefix(t *testing.T) {
	capBytes := httpfetch.MaxErrorBodyBytes
	base := `{"error":"limit_exceeded"}`
	tests := []struct {
		name string
		size int
	}{
		{name: "exactly the cap", size: capBytes},
		{name: "one past the cap", size: capBytes + 1},
		{name: "well past the cap", size: capBytes * 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			body := base + strings.Repeat("x", tt.size-len(base))
			must.Len(body, tt.size)
			srv, calls := alwaysCapped(t, body)
			c := newTestClient(t, srv.URL, "")

			_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			must.Error(err)
			is.Contains(err.Error(), "limit_exceeded", "the code survives a body past the prefix")
			is.Equal(int32(1), calls.Load(), "a long body must not fall back to retrying")
		})
	}
}

// TestTerminalCapBodyGzipped pins that a compressed response is decoded before
// classification, not treated as opaque bytes.
func TestTerminalCapBodyGzipped(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"error":"limit_exceeded"}`))
	must.NoError(zw.Close())

	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(gz.Bytes())
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.Error(err)
	is.Contains(err.Error(), "limit_exceeded")
	is.Equal(int32(1), calls.Load())
}

// TestTerminalCapBodyChunked pins that a chunked response (no Content-Length)
// is read to EOF and handled like a fixed-length one.
func TestTerminalCapBodyChunked(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fl, _ := w.(http.Flusher)
		for _, part := range []string{`{"error":`, `"limit_`, `exceeded"}`} {
			_, _ = io.WriteString(w, part)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.Error(err)
	is.Contains(err.Error(), "limit_exceeded")
	is.Equal(int32(1), calls.Load())
}

// TestTerminalCapBodyTruncatedMidValue pins the documented fallback: a single
// JSON value longer than the prefix is cut in half, fails to decode, and takes
// the ordinary retry path with no code echoed.
func TestTerminalCapBodyTruncatedMidValue(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	body := `{"error":"limit_exceeded","pad":"` + strings.Repeat("a", httpfetch.MaxErrorBodyBytes) + `"}`
	srv, calls := alwaysCapped(t, body)
	c := newTestClient(t, srv.URL, "")

	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.Error(err)
	is.Equal("upstream status 429", err.Error(), "a value the cap cuts in half cannot classify")
	is.Equal(int32(3), calls.Load(), "and takes the ordinary retry path")
}

// TestErrorBodyOn200IsAnError covers the parity gap with the other three
// providers: a 200 carrying an error must fail the lookup, never read as a
// genuine empty answer, which is cached cell-wide and never retried downstream.
func TestErrorBodyOn200IsAnError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "identifier code is echoed", body: `{"error":"limit_exceeded"}`, want: "upstream returned an error response: limit_exceeded"},
		{name: "unrecognised text is not echoed", body: `{"error":"lat=51.4774 key=SUPERSECRET"}`, want: "upstream returned an error response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			calls := &atomic.Int32{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL, "")
			got, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			must.Error(err)
			is.Nil(got)
			is.Equal(tt.want, err.Error())
			is.NotContains(err.Error(), "51.4774")
			is.NotContains(err.Error(), "SUPERSECRET")
			is.Equal(int32(1), calls.Load(), "a 200 is not retried")
		})
	}
}

// TestErrorWithoutFeaturesIsAFailure pins the result-count gate's failure side.
func TestErrorWithoutFeaturesIsAFailure(t *testing.T) {
	tests := []struct {
		name   string
		rawErr string
	}{
		{name: "non-empty object", rawErr: `{"code":"limit_exceeded"}`},
		{name: "non-empty array", rawErr: `["limit_exceeded"]`},
		{name: "number", rawErr: `7`},
		{name: "true", rawErr: `true`},
		{name: "number past float64", rawErr: `1e999`},
		{name: "number past float64 again", rawErr: `1e309`},
		{name: "negative number past float64", rawErr: `-1e999`},
		{name: "number past float64 nested", rawErr: `{"code":1e999}`},
		{name: "huge integer", rawErr: strings.Repeat("9", 400)},
		{name: "number at the float64 edge", rawErr: `1e308`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"error":` + tt.rawErr + `}`))
			}))
			defer srv.Close()

			got, err := newTestClient(t, srv.URL, "").Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			must.Error(err, "an error with no features is a failure, never a cacheable empty")
			is.Nil(got)
		})
	}
}

func TestEmptyErrorFieldIsAGenuineEmpty(t *testing.T) {
	tests := []struct {
		name   string
		rawErr string
	}{
		{name: "null", rawErr: `null`},
		{name: "false", rawErr: `false`},
		{name: "zero", rawErr: `0`},
		{name: "empty string", rawErr: `""`},
		{name: "empty object", rawErr: `{}`},
		{name: "empty array", rawErr: `[]`},
		{name: "empty object with whitespace", rawErr: `{ }`},
		{name: "empty array with whitespace", rawErr: `[ ]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"error":` + tt.rawErr + `}`))
			}))
			defer srv.Close()

			got, err := newTestClient(t, srv.URL, "").Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			must.NoError(err, "a zero error value is a genuine no-match")
			is.Empty(got)
		})
	}
}

// TestStringErrorWithFeaturesIsServed pins the one case the features-aware rule
// exists for: a body that carries an error string and a usable feature is
// served, not turned into a 503.
func TestStringErrorWithFeaturesIsServed(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"limit_exceeded","features":[{"type":"Feature","properties":{"name":"Good"},"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}]}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL, "").Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("Good", got[0].Address.Name)
}

// TestErrorWithUnusableFeaturesIsAFailure pins the result-count gate: a feature
// the mapping drops leaves no usable result, so a set error fails the lookup.
func TestErrorWithUnusableFeaturesIsAFailure(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"limit_exceeded","features":[{"type":"Feature","properties":{"name":"Bad"},"geometry":{"type":"Point","coordinates":[999,999]}}]}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL, "").Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
	must.Error(err, "an unusable feature must not settle the lookup as an empty")
	is.Nil(got)
}

// TestNonStringErrorFieldIsServed pins that the error field is raw: a 200 whose
// error is not a string must not fail the decode, or a body that still carries
// features would regress from an answer to a 503.
func TestNonStringErrorFieldIsServed(t *testing.T) {
	feature := `{"type":"Feature","properties":{"name":"Good"},"geometry":{"type":"Point","coordinates":[-0.061,51.4774]}}`
	tests := []struct {
		name   string
		rawErr string
	}{
		{name: "false", rawErr: `false`},
		{name: "number", rawErr: `42`},
		{name: "object", rawErr: `{"code":"x"}`},
		{name: "array", rawErr: `[]`},
		{name: "empty string", rawErr: `""`},
		{name: "null", rawErr: `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			body := `{"error":` + tt.rawErr + `,"features":[` + feature + `]}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL, "")
			got, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})
			must.NoError(err, "a non-string error field must not fail the decode")
			must.Len(got, 1)
			is.Equal("Good", got[0].Address.Name)
		})
	}
}

// TestCapBodyOnNon429IsRetried pins the status gate: a cap code only ends the
// call on a 429, so a 5xx carrying the same body keeps its retries.
func TestCapBodyOnNon429IsRetried(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"limit_exceeded"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})

	must.Error(err)
	is.Equal("upstream status 503", err.Error(), "the cap code is not reported for a non-429")
	is.Equal(int32(3), calls.Load(), "a 5xx keeps its retries")
}

// TestRateLimitedRetryAfterHonoured pins that a non-terminal 429 still waits
// the provider's Retry-After before the retry.
func TestRateLimitedRetryAfterHonoured(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv, calls := cappedUpstream(t, `{"error":"rate_limited"}`, "1")
	c := newTestClient(t, srv.URL, "")

	start := time.Now()
	_, err := c.Reverse(t.Context(), domain.ReverseQuery{Coordinates: mustCoords(t, 51.4774, -0.061)})

	must.NoError(err)
	is.Equal(int32(2), calls.Load())
	is.GreaterOrEqual(time.Since(start), time.Second, "the retry waits at least the provider's Retry-After")
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
