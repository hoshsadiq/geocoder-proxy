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
)

// Compile-time guard.
var _ port.Geocoder = (*Client)(nil)

func init() {
	registry.Register(providerName, func(cfg registry.Config) (port.Geocoder, error) {
		return New(cfg)
	})
}

// Client calls a Nominatim-shaped upstream.
type Client struct {
	name      string
	base      string
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
// unidentified clients. An empty BaseURL uses the public endpoint.
func New(cfg registry.Config, opts ...Option) (*Client, error) {
	if cfg.UserAgent == "" {
		return nil, fmt.Errorf("%s: UserAgent is required by the Nominatim usage policy", providerName)
	}
	c := &Client{
		name:      providerName,
		base:      cfg.BaseURL,
		userAgent: cfg.UserAgent,
		http:      &http.Client{Timeout: 10 * time.Second},
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

// Reverse resolves the place nearest to the query point. Nominatim returns
// at most one place per reverse call.
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
	v.Set("format", "jsonv2")
	v.Set("addressdetails", "1")
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}

	var places []place
	if err := c.get(ctx, "/search", v, &places); err != nil {
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
	headers := map[string]string{"User-Agent": c.userAgent}
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), headers, out); err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	return nil
}

// place is the Nominatim wire shape. Lat/Lon arrive as strings.
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
	State         string `json:"state"`
	Country       string `json:"country"`
	CountryCode   string `json:"country_code"`
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

// canonicalOsmType maps Nominatim's word forms to the single-letter form
// (N, W, R) the domain and the Photon wire contract use.
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
