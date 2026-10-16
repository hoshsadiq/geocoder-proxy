// Package geoapify is the provider adapter for Geoapify's Geocoding API.
// The response is Geoapify's own `results` array (requested with
// format=json; their default is GeoJSON), mapped into the domain here.
// Geoapify does not return OSM identifiers in geocoding results, so those
// domain fields stay empty.
package geoapify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/internal/httpfetch"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

const (
	defaultBaseURL = "https://api.geoapify.com"
	providerName   = "geoapify"
)

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

// Config is this provider's configuration. It is decoded from operator
// config (koanf tags) and validated before use; unknown keys are rejected
// at load time. It carries a credential: never log or serialise it. The API key is sent as the apiKey query parameter and is required.
type Config struct {
	BaseURL       string  `koanf:"base_url"`
	APIKey        string  `koanf:"api_key"`
	RatePerSecond float64 `koanf:"rate_per_second"`
	DailyQuota    int     `koanf:"daily_quota"`
}

// Validate checks the provider's own requirements.
func (c *Config) Validate() error {
	var missing error
	if c.APIKey == "" {
		missing = errors.New("api_key is required")
	}
	return errors.Join(
		missing,
		registry.ValidateLimits(c.RatePerSecond, c.DailyQuota),
		registry.ValidateBaseURL(c.BaseURL),
	)
}

// Rate is the configured requests-per-second pacing.
func (c *Config) Rate() float64 { return c.RatePerSecond }

// Quota is the configured daily call cap; zero means unlimited.
func (c *Config) Quota() int { return c.DailyQuota }

// Client calls the Geoapify Geocoding API.
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

// New builds the client. The API key is sent as the apiKey query parameter
// (Geoapify's documented auth) and is required. An empty BaseURL uses the
// public endpoint.
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

func (c *Client) Name() string { return c.name }

// Reverse resolves the places nearest to the query point.
func (c *Client) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("lat", strconv.FormatFloat(q.Coordinates.Lat(), 'f', -1, 64))
	v.Set("lon", strconv.FormatFloat(q.Coordinates.Lon(), 'f', -1, 64))
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return c.get(ctx, "/v1/geocode/reverse", v)
}

// Forward resolves places matching free text.
func (c *Client) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("text", q.Text)
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return c.get(ctx, "/v1/geocode/search", v)
}

func (c *Client) get(ctx context.Context, path string, v url.Values) ([]domain.Result, error) {
	v.Set("apiKey", c.apiKey)
	// format=json is explicit: the API's default is GeoJSON, which would
	// decode to an empty results array and read as a genuine "nothing here".
	v.Set("format", "json")
	var body response
	// The router prefixes provider errors with the provider name.
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), nil, &body, nil); err != nil {
		return nil, err
	}
	if body.Error != "" {
		return nil, fmt.Errorf("upstream returned an error response: %s", body.Error)
	}
	if body.Results == nil {
		// The shape we asked for (format=json) carries a results key; its
		// absence means the upstream answered in another shape, which must
		// not read as a genuine "nothing here".
		return nil, errors.New("unexpected response shape: no results key")
	}
	results := make([]domain.Result, 0, len(*body.Results))
	for _, r := range *body.Results {
		if result, ok := r.toResult(); ok {
			results = append(results, result)
		}
	}
	return results, nil
}

// response is the Geoapify Geocoding API wire shape (format=json): a flat
// results array, or an error object on failure. Results is a pointer so an
// absent key (a shape change, or a format=json that never took effect) is
// distinguishable from a genuinely empty day.
type response struct {
	Results *[]entry `json:"results"`
	Error   string   `json:"error"`
}

type entry struct {
	Name        string   `json:"name"`
	HouseNumber string   `json:"housenumber"`
	Street      string   `json:"street"`
	Suburb      string   `json:"suburb"`
	District    string   `json:"district"`
	City        string   `json:"city"`
	County      string   `json:"county"`
	State       string   `json:"state"`
	Postcode    string   `json:"postcode"`
	Country     string   `json:"country"`
	CountryCode string   `json:"country_code"`
	Lat         *float64 `json:"lat"`
	Lon         *float64 `json:"lon"`
	ResultType  string   `json:"result_type"`
}

// toResult maps one entry. An entry without usable coordinates is dropped:
// (0,0) is a real place in the Gulf of Guinea, so a missing coordinate must
// not become one.
func (e entry) toResult() (domain.Result, bool) {
	if e.Lat == nil || e.Lon == nil {
		return domain.Result{}, false
	}
	coords, err := domain.NewCoordinates(*e.Lat, *e.Lon)
	if err != nil {
		return domain.Result{}, false
	}
	return domain.Result{
		Coordinates: coords,
		Address: domain.Address{
			Name:        e.Name,
			Type:        e.ResultType,
			Street:      e.Street,
			HouseNumber: e.HouseNumber,
			Postcode:    e.Postcode,
			City:        e.City,
			District:    firstOf(e.District, e.Suburb),
			County:      e.County,
			State:       e.State,
			Country:     e.Country,
			CountryCode: strings.ToUpper(e.CountryCode),
		},
	}, true
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
