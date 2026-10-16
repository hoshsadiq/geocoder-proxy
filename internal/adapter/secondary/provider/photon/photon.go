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

// terminalCapCode is ChibiGeo's documented plan cap: the only geocoding error
// its docs say waiting cannot clear.
const terminalCapCode = "limit_exceeded"

// isTerminalCap reports whether a non-200 response is that cap on a 429. The
// first JSON value is decoded, so trailing bytes past it do not matter.
func isTerminalCap(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&errBody); err != nil {
		return false
	}
	return errBody.Error == terminalCapCode
}

func (c *Client) get(ctx context.Context, path string, v url.Values) ([]domain.Result, error) {
	headers := map[string]string{}
	if c.apiKey != "" {
		headers["X-Api-Key"] = c.apiKey
	}
	var fc featureCollection
	// The router prefixes provider errors with the provider name.
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), headers, &fc, isTerminalCap); err != nil {
		if errors.Is(err, domain.ErrProviderTerminal) {
			return nil, fmt.Errorf("%w: %s", err, terminalCapCode)
		}
		return nil, err
	}
	results := make([]domain.Result, 0, len(fc.Features))
	for _, f := range fc.Features {
		if r, ok := f.toResult(); ok {
			results = append(results, r)
		}
	}
	// Guards ChibiGeo's wrapper, not Photon: Photon answers 200 with a
	// FeatureCollection and puts errors in `message` on a non-200.
	if fc.Error != nil && *fc.Error != "" && len(results) == 0 {
		return nil, errors.New("upstream returned an error response")
	}
	return results, nil
}

// featureCollection is the Photon wire shape.
type featureCollection struct {
	Features []feature `json:"features"`
	Error    *string   `json:"error"`
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
