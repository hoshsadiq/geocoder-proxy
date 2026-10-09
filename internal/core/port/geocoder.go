package port

import (
	"context"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// Geocoder is the outgoing port every upstream provider adapter implements:
// one geocoding backend (ChibiGeo, Geoapify, LocationIQ, Nominatim, a future
// local dataset). Implementations translate the provider's response into
// domain types and must not rate-limit themselves; pacing and quotas are the
// router's job.
//
// Contract:
//   - An empty slice with a nil error is a genuine "nothing here". Any
//     failure, including "this provider does not offer this operation", must
//     be an error, or the router caches the empty answer and never fails over.
//   - Returned errors must not contain coordinates or query text; error text
//     can reach logs.
//   - Populate Address.CountryCode whenever the provider returns one;
//     providers localise the country name, and the Photon adapter
//     canonicalises names from the code so failover cannot fragment country
//     attribution downstream.
type Geocoder interface {
	// Name identifies the provider in config, logs and metric labels.
	Name() string

	// Reverse resolves the places nearest to the query point, in the order
	// the caller should see them (the first result drives the cache reuse
	// distance check). Honour Limit, Radius and DistanceSort where the
	// provider supports them.
	Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error)

	// Forward resolves places matching free text.
	Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error)
}
