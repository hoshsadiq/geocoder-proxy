// Package locationiq is the provider adapter for LocationIQ. The response
// is LocationIQ's flat JSON (Nominatim-compatible shape), mapped into the
// domain here. Auth is the key query parameter.
package locationiq

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
	defaultBaseURL = "https://us1.locationiq.com"
	providerName   = "locationiq"
)

// Compile-time guard.
var _ port.Geocoder = (*Client)(nil)

func init() {
	registry.Register(providerName, func(cfg registry.Config) (port.Geocoder, error) {
		return New(cfg)
	})
}

// Client calls the LocationIQ API.
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

// New builds the client. The API key is sent as the key query parameter
// (LocationIQ's documented auth) and is required. An empty BaseURL uses the
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

// Reverse resolves the place nearest to the query point. LocationIQ returns
// at most one place per reverse call.
func (c *Client) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("lat", strconv.FormatFloat(q.Coordinates.Lat(), 'f', -1, 64))
	v.Set("lon", strconv.FormatFloat(q.Coordinates.Lon(), 'f', -1, 64))
	v.Set("format", "json")

	var place place
	if err := c.get(ctx, "/v1/reverse", v, &place); err != nil {
		return nil, err
	}
	if place.isEmpty() {
		return nil, nil
	}
	result, err := place.toResult()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	return []domain.Result{result}, nil
}

// Forward resolves places matching free text.
func (c *Client) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	v := url.Values{}
	v.Set("q", q.Text)
	v.Set("format", "json")
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}

	var places []place
	if err := c.get(ctx, "/v1/search", v, &places); err != nil {
		return nil, err
	}
	results := make([]domain.Result, 0, len(places))
	for _, p := range places {
		result, err := p.toResult()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func (c *Client) get(ctx context.Context, path string, v url.Values, out any) error {
	v.Set("key", c.apiKey)
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), nil, out); err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	return nil
}

// place is the LocationIQ wire shape (Nominatim-compatible). Lat/Lon arrive
// as strings.
type place struct {
	OsmID       int64   `json:"osm_id"`
	OsmType     string  `json:"osm_type"`
	Lat         string  `json:"lat"`
	Lon         string  `json:"lon"`
	Name        string  `json:"name"`
	AddressType string  `json:"addresstype"`
	Address     address `json:"address"`
}

type address struct {
	Name        string `json:"name"`
	HouseNumber string `json:"house_number"`
	Road        string `json:"road"`
	Postcode    string `json:"postcode"`
	City        string `json:"city"`
	Town        string `json:"town"`
	Village     string `json:"village"`
	Suburb      string `json:"suburb"`
	County      string `json:"county"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
}

// isEmpty reports a no-match reverse response (an object without an osm id).
func (p place) isEmpty() bool {
	return p.OsmID == 0 && p.Lat == "" && p.Lon == ""
}

func (p place) toResult() (domain.Result, error) {
	lat, err := strconv.ParseFloat(p.Lat, 64)
	if err != nil {
		return domain.Result{}, fmt.Errorf("unparseable latitude %q in response", p.Lat)
	}
	lon, err := strconv.ParseFloat(p.Lon, 64)
	if err != nil {
		return domain.Result{}, fmt.Errorf("unparseable longitude %q in response", p.Lon)
	}
	coords, err := domain.NewCoordinates(lat, lon)
	if err != nil {
		return domain.Result{}, fmt.Errorf("out-of-range coordinates in response: %w", err)
	}

	name := p.Name
	if name == "" {
		name = p.Address.Name
	}
	return domain.Result{
		Coordinates: coords,
		Address: domain.Address{
			Name:        name,
			Type:        p.AddressType,
			Street:      p.Address.Road,
			HouseNumber: p.Address.HouseNumber,
			Postcode:    p.Address.Postcode,
			City:        firstOf(p.Address.City, p.Address.Town, p.Address.Village),
			District:    p.Address.Suburb,
			County:      p.Address.County,
			State:       p.Address.State,
			Country:     p.Address.Country,
			CountryCode: strings.ToUpper(p.Address.CountryCode),
			OsmID:       p.OsmID,
			OsmType:     canonicalOsmType(p.OsmType),
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

// canonicalOsmType maps word forms to the single-letter form (N, W, R) the
// domain and the Photon wire contract use.
func canonicalOsmType(raw string) string {
	switch raw {
	case "node":
		return "N"
	case "way":
		return "W"
	case "relation":
		return "R"
	default:
		return ""
	}
}
