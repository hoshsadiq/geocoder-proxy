package port

import (
	"context"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// GeocodeService is the incoming port: the geocoding API the service offers.
// Primary adapters (the Photon HTTP API) depend on this, never on the
// concrete router.
type GeocodeService interface {
	// Reverse resolves the address nearest to the query point.
	// A Result with Found == false and a nil error means "genuinely nothing
	// here"; an error means the lookup failed and must not be treated as an
	// answer.
	Reverse(ctx context.Context, q domain.ReverseQuery) (domain.Result, error)

	// Forward resolves places matching free text. An empty slice with a nil
	// error is a genuine "no match".
	Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error)
}
