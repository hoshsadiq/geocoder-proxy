// Package geoapify is the provider adapter for Geoapify's Geocoding API.
// The response is Geoapify's own `results` array (not GeoJSON), mapped into
// the domain here. Geoapify does not return OSM identifiers in geocoding
// results, so those domain fields stay empty.
package geoapify

import (
	"context"
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

// Compile-time guard.
var _ port.Geocoder = (*Client)(nil)

func init() {
	registry.Register(providerName, func(cfg registry.Config) (port.Geocoder, error) {
		return New(cfg)
	})
}

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
func New(cfg registry.Config, opts ...Option) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%s: APIKey is required", providerName)
	}
	c := &Client{
		name:   providerName,
		base:   cfg.BaseURL,
		apiKey: cfg.APIKey,
		http:   &http.Client{Timeout: 10 * time.Second},
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
	var body response
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), nil, &body); err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	results := make([]domain.Result, 0, len(body.Results))
	for _, r := range body.Results {
		result, err := r.toResult()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// response is the Geoapify Geocoding API wire shape: a flat results array.
type response struct {
	Results []entry `json:"results"`
}

type entry struct {
	Name        string  `json:"name"`
	HouseNumber string  `json:"housenumber"`
	Street      string  `json:"street"`
	Suburb      string  `json:"suburb"`
	District    string  `json:"district"`
	City        string  `json:"city"`
	County      string  `json:"county"`
	State       string  `json:"state"`
	Postcode    string  `json:"postcode"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	ResultType  string  `json:"result_type"`
}

func (e entry) toResult() (domain.Result, error) {
	coords, err := domain.NewCoordinates(e.Lat, e.Lon)
	if err != nil {
		return domain.Result{}, fmt.Errorf("out-of-range coordinates in response: %w", err)
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
