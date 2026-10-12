// Package photon is the primary HTTP adapter: it serves the Photon API
// shape Dawarich already speaks, translating requests into core calls and
// domain results into a GeoJSON FeatureCollection.
package photon

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Compile-time guard.
var _ http.Handler = (*Handler)(nil)

// Handler serves the Photon API over a core geocoding service.
type Handler struct {
	svc     port.GeocodeService
	mux     *http.ServeMux
	regions display.Namer
	logger  *slog.Logger
}

// New builds the handler. regionNames canonicalises country names from
// country codes (display.English.Regions()); logger receives request logs
// (slog.Default() when nil).
func New(svc port.GeocodeService, regionNames display.Namer, logger *slog.Logger) *Handler {
	h := &Handler{svc: svc, regions: regionNames, logger: logger}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /reverse", h.reverse)
	mux.HandleFunc("GET /api", h.forward)
	h.mux = mux
	return h
}

// ServeHTTP logs and dispatches the request. The log line carries the
// escaped path so a percent-encoded query string cannot smuggle
// coordinates or search text into the log.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	h.mux.ServeHTTP(rw, r)
	h.logger.InfoContext(r.Context(), "request",
		"method", r.Method,
		"path", r.URL.EscapedPath(),
		"status", rw.status,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// maxResultLimit caps the limit parameter. Photon's own servers cap
// results, and an unbounded limit lets one request fill the cache with an
// arbitrarily large answer set.
const maxResultLimit = 50

func (h *Handler) reverse(w http.ResponseWriter, r *http.Request) {
	lat, errLat := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, errLon := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	coords, errCoords := domain.NewCoordinates(lat, lon)
	if errLat != nil || errLon != nil || errCoords != nil {
		writeError(w, http.StatusBadRequest, "invalid or missing lat/lon")
		return
	}

	q := domain.ReverseQuery{Coordinates: coords}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Limit = min(n, maxResultLimit)
		}
	}
	if v := r.URL.Query().Get("radius"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			q.Radius = f
		}
	}
	q.DistanceSort = r.URL.Query().Get("distance_sort") == "true"

	results, err := h.svc.Reverse(r.Context(), q)
	h.respond(w, r, results, err)
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	q := domain.ForwardQuery{Text: r.URL.Query().Get("q")}
	if q.Text == "" {
		writeError(w, http.StatusBadRequest, "missing q parameter")
		return
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Limit = min(n, maxResultLimit)
		}
	}

	results, err := h.svc.Forward(r.Context(), q)
	h.respond(w, r, results, err)
}

// respond maps the core outcome onto the wire. The failure status is
// always one the geocoder gem Dawarich uses raises as transient
// (400/401/402/429/503): any other status with a JSON body parses into an
// empty answer, which Dawarich marks final and never retries — the failure
// this service exists to prevent. A genuine empty is a 200 with an empty
// FeatureCollection.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request, results []domain.Result, err error) {
	if err != nil {
		// Provider failure outranks the cancellation check: a provider's
		// HTTP timeout wraps context.DeadlineExceeded into the chain too,
		// and a future in-process request deadline must never turn that
		// shape into an empty 200.
		switch {
		case errors.Is(err, domain.ErrAllProvidersFailed), errors.Is(err, domain.ErrNoProviders):
			// Provider error text is coordinate-free by contract, so it is
			// safe to log.
			h.logger.WarnContext(r.Context(), "all providers failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "geocoding temporarily unavailable")
		case r.Context().Err() != nil:
			// The caller is gone; writing a status nobody reads only adds a
			// scary log line. Debug, not error.
			h.logger.DebugContext(r.Context(), "client gone before answer", "error", r.Context().Err())
		case errors.Is(err, domain.ErrInvalidQuery), errors.Is(err, domain.ErrInvalidCoordinates):
			writeError(w, http.StatusBadRequest, "invalid query")
		default:
			// Unknown failure: still 503, never a non-raising status.
			h.logger.ErrorContext(r.Context(), "lookup failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "geocoding temporarily unavailable")
		}
		return
	}

	fc := featureCollection{Type: "FeatureCollection"}
	for _, result := range results {
		fc.Features = append(fc.Features, h.toFeature(result))
	}
	if fc.Features == nil {
		fc.Features = []feature{}
	}
	writeJSON(w, http.StatusOK, fc)
}

func (h *Handler) toFeature(r domain.Result) feature {
	country := r.Address.Country
	if code := r.Address.CountryCode; code != "" && h.regions != nil {
		// Providers localise country names; the code is stable, so the
		// English name comes from the code. Dawarich resolves countries by
		// exact name against its own seed, which CLDR disagrees with for 28
		// codes; those take the Dawarich name verbatim.
		if name, ok := dawarichCountryNames[code]; ok {
			country = name
		} else if region, err := language.ParseRegion(code); err == nil {
			if name := h.regions.Name(region); name != "" {
				country = name
			}
		}
	}
	return feature{
		Type: "Feature",
		Properties: properties{
			Name:        r.Address.Name,
			Type:        r.Address.Type,
			Street:      r.Address.Street,
			HouseNumber: r.Address.HouseNumber,
			Postcode:    r.Address.Postcode,
			City:        r.Address.City,
			Locality:    r.Address.Locality,
			District:    r.Address.District,
			County:      r.Address.County,
			State:       r.Address.State,
			Country:     country,
			CountryCode: r.Address.CountryCode,
			OsmID:       r.Address.OsmID,
			OsmType:     r.Address.OsmType,
			OsmKey:      r.Address.OsmKey,
			OsmValue:    r.Address.OsmValue,
		},
		Geometry: geometry{
			Type:        "Point",
			Coordinates: []float64{r.Coordinates.Lon(), r.Coordinates.Lat()},
		},
	}
}

// featureCollection is the Photon wire shape. Absent properties are
// omitted, matching Photon's own responses (and mattering for Ruby
// consumers, where an empty string is truthy).
type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type feature struct {
	Type       string     `json:"type"`
	Properties properties `json:"properties"`
	Geometry   geometry   `json:"geometry"`
}

type geometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type properties struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Street      string `json:"street,omitempty"`
	HouseNumber string `json:"housenumber,omitempty"`
	Postcode    string `json:"postcode,omitempty"`
	City        string `json:"city,omitempty"`
	Locality    string `json:"locality,omitempty"`
	District    string `json:"district,omitempty"`
	County      string `json:"county,omitempty"`
	State       string `json:"state,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"countrycode,omitempty"`
	OsmID       int64  `json:"osm_id,omitempty"`
	OsmType     string `json:"osm_type,omitempty"`
	OsmKey      string `json:"osm_key,omitempty"`
	OsmValue    string `json:"osm_value,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
