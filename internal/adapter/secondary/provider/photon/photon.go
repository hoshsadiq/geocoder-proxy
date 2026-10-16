// Package photon is the provider adapter for Photon-shaped upstreams:
// ChibiGeo, komoot's photon.komoot.io, and self-hosted Photon instances.
// The response is already a GeoJSON FeatureCollection in the shape the
// primary adapter serves, so the mapping is close to identity; the
// normalisation that does happen (canonical OSM type letters, uppercase
// country code) matches the domain contract.
package photon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/internal/httpfetch"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/internal/jsonnum"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

const (
	// defaultBaseURL is ChibiGeo's public endpoint.
	defaultBaseURL = "https://app.chibigeo.com/v1/photon"
	providerName   = "chibigeo"
)

// Compile-time guard.
var _ port.Geocoder = (*Client)(nil)

func init() {
	registry.Register(providerName, registry.Registration{
		NewConfig: func() registry.Config { return &Config{} },
		Build: func(c registry.Config) (port.Geocoder, error) {
			cfg, ok := c.(*Config)
			if !ok || cfg == nil {
				return nil, fmt.Errorf("%s: unexpected config type %T", providerName, c)
			}
			return New(cfg)
		},
	})
}

// Config is this provider's configuration, registered as "chibigeo" (the
// provider it fronts by default; the type is Photon-shaped because any
// Photon-compatible upstream can serve it). It is decoded from operator
// config (koanf tags) and validated before use; unknown keys are rejected
// at load time. It carries a credential: never log or serialise it.
type Config struct {
	BaseURL       string  `koanf:"base_url"`
	APIKey        string  `koanf:"api_key"`
	RatePerSecond float64 `koanf:"rate_per_second"`
	DailyQuota    int     `koanf:"daily_quota"`
}

// Validate checks the provider's own requirements. Photon-shaped upstreams
// need no credentials (the API key is optional, for ChibiGeo).
func (c *Config) Validate() error {
	return errors.Join(
		registry.ValidateLimits(c.RatePerSecond, c.DailyQuota),
		registry.ValidateBaseURL(c.BaseURL),
	)
}

// Rate is the configured requests-per-second pacing.
func (c *Config) Rate() float64 { return c.RatePerSecond }

// Quota is the configured daily call cap; zero means unlimited.
func (c *Config) Quota() int { return c.DailyQuota }

// Client calls a Photon-shaped upstream.
type Client struct {
	name   string
	base   string
	apiKey string
	http   *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient injects an HTTP client, e.g. one with a custom timeout or
// transport. The default is a 10-second-timeout client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// New builds the client. An empty BaseURL uses the provider's public
// endpoint. An API key is sent as the X-Api-Key header (ChibiGeo's
// documented auth); empty means unauthenticated (komoot, most self-hosts).
func New(cfg *Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", providerName, err)
	}
	c := &Client{
		name:   providerName,
		base:   strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey: cfg.APIKey,
		http:   httpfetch.NewClient(10 * time.Second),
	}
	if c.base == "" {
		c.base = defaultBaseURL
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Name identifies the provider in config, logs and metric labels.
func (c *Client) Name() string { return c.name }

// Reverse resolves the places nearest to the query point.
func (c *Client) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("lat", strconv.FormatFloat(q.Coordinates.Lat(), 'f', -1, 64))
	v.Set("lon", strconv.FormatFloat(q.Coordinates.Lon(), 'f', -1, 64))
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Radius > 0 {
		v.Set("radius", strconv.FormatFloat(q.Radius, 'f', -1, 64))
	}
	if q.DistanceSort {
		v.Set("distance_sort", "true")
	}
	return c.get(ctx, "/reverse", v)
}

// Forward resolves places matching free text.
func (c *Client) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("q", q.Text)
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return c.get(ctx, "/api", v)
}

// terminalCodes are the ChibiGeo error codes for a plan cap that waiting
// cannot clear. A wrong entry only costs the retries we already had, so the
// list errs towards including a terminal code.
var terminalCodes = map[string]struct{}{
	// Confirmed: the documented plan cap, seen live as 429 {"error":"limit_exceeded"}.
	"limit_exceeded": {},
	// Inferred: an exhausted allowance, not observed.
	"quota_exceeded": {},
	// Terminal by nature (payment is needed), not observed.
	"subscription_past_due": {},
}

// terminalResponse classifies a non-200 response. Only a 429 carrying one of
// the known terminal codes is terminal; everything else, including an unrecognised
// body and any other status, takes the ordinary retry path. httpfetch caps how
// much of the body it offers, so the first JSON value is decoded rather than the
// whole buffer unmarshalled: trailing bytes past the value do not matter, and a
// value the cap cuts in half fails to decode and falls back to retrying.
func terminalResponse(statusCode int, body []byte) (string, bool) {
	if statusCode != http.StatusTooManyRequests {
		return "", false
	}
	var shape struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&shape); err != nil {
		return "", false
	}
	if _, ok := terminalCodes[shape.Error]; !ok {
		return "", false
	}
	return shape.Error, true
}

func (c *Client) get(ctx context.Context, path string, v url.Values) ([]domain.Result, error) {
	headers := map[string]string{}
	if c.apiKey != "" {
		headers["X-Api-Key"] = c.apiKey
	}
	var fc featureCollection
	// The router prefixes provider errors with the provider name.
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), headers, &fc, terminalResponse); err != nil {
		return nil, err
	}
	// A 200 that carries an error and no features is a failure, not an empty
	// answer: an empty answer is cached and treated as final downstream, so a
	// swallowed error would lose the point. Features win when both are present.
	if len(fc.Features) == 0 && errorFieldSet(fc.Error) {
		if msg := errorMessage(fc.Error); msg != "" && httpfetch.SafeCode(msg) {
			return nil, fmt.Errorf("upstream returned an error response: %s", msg)
		}
		return nil, errors.New("upstream returned an error response")
	}
	results := make([]domain.Result, 0, len(fc.Features))
	for _, f := range fc.Features {
		if r, ok := f.toResult(); ok {
			results = append(results, r)
		}
	}
	return results, nil
}

// featureCollection is the Photon wire shape.
type featureCollection struct {
	Features []feature       `json:"features"`
	Error    json.RawMessage `json:"error"`
}

// The error field is raw because a 200 with a non-string error is not a decode
// failure.
func errorMessage(raw json.RawMessage) string {
	var msg string
	if len(raw) == 0 || json.Unmarshal(raw, &msg) != nil || msg == "" {
		return ""
	}
	return msg
}

// errorFieldSet reports whether a body's error field is set to a non-empty
// value. An absent, null, false, zero, empty string or empty object/array is
// not an error; anything else is, so an unrecognised error shape fails loudly
// instead of reading as a genuine no-match and being cached.
func errorFieldSet(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch val := v.(type) {
	case nil:
		return false
	case bool:
		return val
	case float64:
		return val != 0
	case string:
		return val != ""
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}

type feature struct {
	Properties properties `json:"properties"`
	Geometry   struct {
		Coordinates []float64 `json:"coordinates"` // [lon, lat]
	} `json:"geometry"`
}

type properties struct {
	// LocationIQ-shaped and some Photon-shaped upstreams quote numeric ids;
	// json.Number accepts both forms.
	OsmID       json.Number `json:"osm_id"`
	OsmType     string      `json:"osm_type"`
	OsmKey      string      `json:"osm_key"`
	OsmValue    string      `json:"osm_value"`
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Street      string      `json:"street"`
	HouseNumber string      `json:"housenumber"`
	Postcode    string      `json:"postcode"`
	City        string      `json:"city"`
	Locality    string      `json:"locality"`
	District    string      `json:"district"`
	County      string      `json:"county"`
	State       string      `json:"state"`
	Country     string      `json:"country"`
	CountryCode string      `json:"countrycode"`
}

func (f feature) toResult() (domain.Result, bool) {
	if len(f.Geometry.Coordinates) != 2 {
		return domain.Result{}, false
	}
	// Provider coordinates are trusted, but a garbage pair must not poison
	// the cache cell: a feature without valid coordinates is dropped.
	coords, err := domain.NewCoordinates(f.Geometry.Coordinates[1], f.Geometry.Coordinates[0])
	if err != nil {
		return domain.Result{}, false
	}

	r := domain.Result{Address: domain.Address{
		Name:        f.Properties.Name,
		Type:        f.Properties.Type,
		Street:      f.Properties.Street,
		HouseNumber: f.Properties.HouseNumber,
		Postcode:    f.Properties.Postcode,
		City:        f.Properties.City,
		Locality:    f.Properties.Locality,
		District:    f.Properties.District,
		County:      f.Properties.County,
		State:       f.Properties.State,
		Country:     f.Properties.Country,
		CountryCode: strings.ToUpper(f.Properties.CountryCode),
		OsmID:       jsonnum.Int64(f.Properties.OsmID),
		OsmType:     domain.CanonicalOsmType(f.Properties.OsmType),
		OsmKey:      f.Properties.OsmKey,
		OsmValue:    f.Properties.OsmValue,
	}, Coordinates: coords}
	return r, true
}
