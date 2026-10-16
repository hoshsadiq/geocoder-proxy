// Package nominatim is the provider adapter for Nominatim-shaped upstreams:
// the public nominatim.openstreetmap.org and self-hosted instances. The
// response is Nominatim's flat JSON, mapped into the domain here.
//
// The public instance's usage policy requires an identifying User-Agent and
// allows an absolute maximum of 1 request per second; the rate limiter is
// the router's concern, the User-Agent is configured here.
package nominatim

import (
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
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

const (
	defaultBaseURL = "https://nominatim.openstreetmap.org"
	providerName   = "nominatim"
	// noMatchError is Nominatim's reverse no-match marker: a 200 response
	// whose body is an error object naming it.
	noMatchError = "Unable to geocode"
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
// at load time. It carries a credential: never log or serialise it. Nominatim requires an identifying User-Agent (its usage policy blocks unidentified clients); the API key is only for hosted instances that require one.
type Config struct {
	BaseURL       string  `koanf:"base_url"`
	APIKey        string  `koanf:"api_key"`
	UserAgent     string  `koanf:"user_agent"`
	RatePerSecond float64 `koanf:"rate_per_second"`
	DailyQuota    int     `koanf:"daily_quota"`
}

// Validate checks the provider's own requirements.
func (c *Config) Validate() error {
	var missing error
	if c.UserAgent == "" {
		missing = errors.New("user_agent is required by the Nominatim usage policy")
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

// Client calls a Nominatim-shaped upstream.
type Client struct {
	name      string
	base      string
	apiKey    string
	userAgent string
	http      *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient injects an HTTP client, e.g. one with a custom timeout or
// transport. The default is a 10-second-timeout client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// New builds the client. UserAgent identifies the application to the
// provider and is required: the public Nominatim usage policy blocks
// unidentified clients. An empty BaseURL uses the public endpoint. APIKey
// is sent as the key query parameter for hosted instances that require one.
func New(cfg *Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", providerName, err)
	}
	c := &Client{
		name:      providerName,
		base:      strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		userAgent: cfg.UserAgent,
		http:      httpfetch.NewClient(10 * time.Second),
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

// Reverse resolves the place nearest to the query point. Nominatim returns
// at most one place per reverse call, or an error object for a no-match.
func (c *Client) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("lat", strconv.FormatFloat(q.Coordinates.Lat(), 'f', -1, 64))
	v.Set("lon", strconv.FormatFloat(q.Coordinates.Lon(), 'f', -1, 64))
	v.Set("format", "jsonv2")
	v.Set("addressdetails", "1")

	var place place
	if err := c.get(ctx, "/reverse", v, &place); err != nil {
		return nil, err
	}
	if msg := place.errorMessage(); msg != "" {
		if msg == noMatchError {
			return nil, nil
		}
		return nil, fmt.Errorf("upstream returned an error response: %s", msg)
	}
	if place.isEmpty() {
		return nil, nil
	}
	result, err := place.toResult()
	if err != nil {
		return nil, err
	}
	return []domain.Result{result}, nil
}

// Forward resolves places matching free text.
func (c *Client) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("q", q.Text)
	v.Set("format", "jsonv2")
	v.Set("addressdetails", "1")
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}

	// The body is an array of places, or an error object.
	var raw json.RawMessage
	if err := c.get(ctx, "/search", v, &raw); err != nil {
		return nil, err
	}
	var places []place
	if err := json.Unmarshal(raw, &places); err != nil {
		msg := errorBodyMessage(raw)
		if msg == "" {
			return nil, fmt.Errorf("decoding upstream response: %w", err)
		}
		if msg == noMatchError {
			return nil, nil
		}
		return nil, fmt.Errorf("upstream returned an error response: %s", msg)
	}
	results := make([]domain.Result, 0, len(places))
	for _, p := range places {
		result, err := p.toResult()
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// errorBodyMessage extracts the message when the body is an error object;
// empty when it is not. An empty or unparseable message is still an error
// response, not a no-match.
func errorBodyMessage(raw json.RawMessage) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Error) == 0 {
		return ""
	}
	var msg string
	if json.Unmarshal(body.Error, &msg) == nil && msg != "" {
		return msg
	}
	return "unrecognised error body"
}

func (c *Client) get(ctx context.Context, path string, v url.Values, out any) error {
	if c.apiKey != "" {
		v.Set("key", c.apiKey)
	}
	headers := map[string]string{"User-Agent": c.userAgent}
	// The router prefixes provider errors with the provider name.
	return httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), headers, out, nil)
}

// place is the Nominatim wire shape. Lat/Lon arrive as strings.
type place struct {
	OsmID       int64           `json:"osm_id"`
	OsmType     string          `json:"osm_type"`
	Lat         string          `json:"lat"`
	Lon         string          `json:"lon"`
	Name        string          `json:"name"`
	AddressType string          `json:"addresstype"`
	Category    string          `json:"category"`
	Type        string          `json:"type"`
	Error       json.RawMessage `json:"error"`
	Address     address         `json:"address"`
}

type address struct {
	HouseNumber   string `json:"house_number"`
	Road          string `json:"road"`
	Postcode      string `json:"postcode"`
	City          string `json:"city"`
	Town          string `json:"town"`
	Village       string `json:"village"`
	Municipality  string `json:"municipality"`
	Suburb        string `json:"suburb"`
	Quarter       string `json:"quarter"`
	Neighbourhood string `json:"neighbourhood"`
	CityDistrict  string `json:"city_district"`
	County        string `json:"county"`
	StateDistrict string `json:"state_district"`
	State         string `json:"state"`
	Country       string `json:"country"`
	CountryCode   string `json:"country_code"`
}

// errorMessage extracts the provider's error message when the response is
// an error object instead of a place. An empty or unparseable message is
// still an error response, not a no-match.
func (p place) errorMessage() string {
	if len(p.Error) == 0 {
		return ""
	}
	var msg string
	if json.Unmarshal(p.Error, &msg) == nil && msg != "" {
		return msg
	}
	return "unrecognised error body"
}

// isEmpty reports a no-match reverse response (an object without an osm id).
func (p place) isEmpty() bool {
	return p.OsmID == 0 && p.Lat == "" && p.Lon == ""
}

func (p place) toResult() (domain.Result, error) {
	lat, err := strconv.ParseFloat(p.Lat, 64)
	if err != nil {
		return domain.Result{}, errors.New("unparseable latitude in response")
	}
	lon, err := strconv.ParseFloat(p.Lon, 64)
	if err != nil {
		return domain.Result{}, errors.New("unparseable longitude in response")
	}
	coords, err := domain.NewCoordinates(lat, lon)
	if err != nil {
		// A broken upstream is not client input, so the request-validation
		// sentinel must not escape here.
		return domain.Result{}, errors.New("out-of-range coordinates in response")
	}

	return domain.Result{
		Coordinates: coords,
		Address: domain.Address{
			Name:        p.Name,
			Type:        p.AddressType,
			Street:      p.Address.Road,
			HouseNumber: p.Address.HouseNumber,
			Postcode:    p.Address.Postcode,
			City:        firstOf(p.Address.City, p.Address.Town, p.Address.Village, p.Address.Municipality),
			Locality:    firstOf(p.Address.Quarter, p.Address.Neighbourhood),
			District:    firstOf(p.Address.CityDistrict, p.Address.Suburb),
			County:      firstOf(p.Address.County, p.Address.StateDistrict),
			State:       p.Address.State,
			Country:     p.Address.Country,
			CountryCode: strings.ToUpper(p.Address.CountryCode),
			OsmID:       p.OsmID,
			OsmType:     domain.CanonicalOsmType(p.OsmType),
			OsmKey:      p.Category,
			OsmValue:    p.Type,
		},
	}, nil
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
