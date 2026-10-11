// Package photon is the provider adapter for Photon-shaped upstreams:
// ChibiGeo, komoot's photon.komoot.io, and self-hosted Photon instances.
// The response is already a GeoJSON FeatureCollection in the shape the
// primary adapter serves, so the mapping is close to identity; the
// normalisation that does happen (canonical OSM type letters, uppercase
// country code) matches the domain contract.
package photon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/internal/httpfetch"
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
	registry.Register(providerName, func(cfg registry.Config) (port.Geocoder, error) {
		return New(cfg)
	})
}

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
func New(cfg registry.Config, opts ...Option) (*Client, error) {
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

func (c *Client) get(ctx context.Context, path string, v url.Values) ([]domain.Result, error) {
	headers := map[string]string{}
	if c.apiKey != "" {
		headers["X-Api-Key"] = c.apiKey
	}
	var fc featureCollection
	if err := httpfetch.Do(ctx, c.http, c.base+path+"?"+v.Encode(), headers, &fc); err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	results := make([]domain.Result, 0, len(fc.Features))
	for _, f := range fc.Features {
		results = append(results, f.toResult())
	}
	return results, nil
}

// featureCollection is the Photon wire shape.
type featureCollection struct {
	Features []feature `json:"features"`
}

type feature struct {
	Properties properties `json:"properties"`
	Geometry   struct {
		Coordinates []float64 `json:"coordinates"` // [lon, lat]
	} `json:"geometry"`
}

type properties struct {
	OsmID       int64  `json:"osm_id"`
	OsmType     string `json:"osm_type"`
	OsmKey      string `json:"osm_key"`
	OsmValue    string `json:"osm_value"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Street      string `json:"street"`
	HouseNumber string `json:"housenumber"`
	Postcode    string `json:"postcode"`
	City        string `json:"city"`
	Locality    string `json:"locality"`
	District    string `json:"district"`
	County      string `json:"county"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
}

func (f feature) toResult() domain.Result {
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
		CountryCode: f.Properties.CountryCode,
		OsmID:       f.Properties.OsmID,
		OsmType:     canonicalOsmType(f.Properties.OsmType),
		OsmKey:      f.Properties.OsmKey,
		OsmValue:    f.Properties.OsmValue,
	}}
	if len(f.Geometry.Coordinates) == 2 {
		// Provider coordinates are trusted, but a garbage pair must not
		// poison the cache cell: drop the result instead.
		if c, err := domain.NewCoordinates(f.Geometry.Coordinates[1], f.Geometry.Coordinates[0]); err == nil {
			r.Coordinates = c
		}
	}
	return r
}

// canonicalOsmType normalises to the single-letter form (N, W, R) the
// domain and the Photon wire contract use. Photon-shaped upstreams already
// return letters; the normalisation matters for word-form providers and is
// a pass-through here.
func canonicalOsmType(raw string) string {
	switch raw {
	case "node", "N":
		return "N"
	case "way", "W":
		return "W"
	case "relation", "R":
		return "R"
	default:
		return ""
	}
}
