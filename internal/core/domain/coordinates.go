package domain

import (
	"fmt"
	"math"
)

// Coordinates is a WGS84 latitude/longitude pair. Build it with
// NewCoordinates: the fields are exported for the provider adapters, but
// callers on the request path must go through the constructor so NaN and
// out-of-range input cannot reach the cache-key and distance math.
//
// A location history is PII, so Coordinates deliberately has no String or
// MarshalText helper: raw coordinates must not end up in logs or metrics by
// convenience.
type Coordinates struct {
	Lat float64
	Lon float64
}

// NewCoordinates validates the ranges and returns the pair. The error names
// the field but not the value: error text can reach logs.
func NewCoordinates(lat, lon float64) (Coordinates, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return Coordinates{}, fmt.Errorf("%w: non-finite value", ErrInvalidCoordinates)
	}
	if lat < -90 || lat > 90 {
		return Coordinates{}, fmt.Errorf("%w: latitude outside [-90, 90]", ErrInvalidCoordinates)
	}
	if lon < -180 || lon > 180 {
		return Coordinates{}, fmt.Errorf("%w: longitude outside [-180, 180]", ErrInvalidCoordinates)
	}
	return Coordinates{Lat: lat, Lon: lon}, nil
}

// DistanceTo returns the great-circle distance to other in metres (haversine).
// Used by the cache reuse rule: an answer is only reused for a request close
// to where the answer itself sits.
func (c Coordinates) DistanceTo(other Coordinates) float64 {
	const earthRadiusM = 6371000.0
	const deg = math.Pi / 180

	dLat := (other.Lat - c.Lat) * deg
	dLon := (other.Lon - c.Lon) * deg
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(c.Lat*deg)*math.Cos(other.Lat*deg)*math.Sin(dLon/2)*math.Sin(dLon/2)
	// a can exceed 1 by float noise near the antipodes; sqrt(1-a) would go NaN.
	a = min(a, 1)
	return 2 * earthRadiusM * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// cellSizeDeg is the cache grid size in degrees: 0.0005° is roughly 55 m,
// about one house plot. Points inside a cell may share a cached answer,
// subject to the distance rule in the router.
const cellSizeDeg = 0.0005

// CellKey identifies the cache cell the coordinates fall in, as integer cell
// indices so the key is exact and free of float formatting surprises.
func (c Coordinates) CellKey() string {
	lat := int64(math.Round(c.Lat / cellSizeDeg))
	lon := int64(math.Round(c.Lon / cellSizeDeg))
	return fmt.Sprintf("%d:%d", lat, lon)
}
