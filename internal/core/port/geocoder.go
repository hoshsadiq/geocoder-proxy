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
type Geocoder interface {
	// Name identifies the provider in config, logs and metric labels.
	Name() string

	Reverse(ctx context.Context, q domain.ReverseQuery) (domain.Result, error)
	Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error)
}
