// Package tokenbucket is the Limiter adapter: a thin wrapper over
// golang.org/x/time/rate so the core never imports a pacing library.
package tokenbucket

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Compile-time guard.
var _ port.Limiter = (*Limiter)(nil)

// Limiter admits calls at a fixed rate with a burst of one: strict pacing,
// which is what free provider quotas want.
type Limiter struct {
	inner *rate.Limiter
}

// New returns a Limiter admitting rps calls per second. rps must be
// positive: at zero or below the limiter would block forever, which is a
// config error and must fail at startup, not under load. Config that wants
// no pacing should leave the provider's Limiter nil instead.
func New(rps float64) *Limiter {
	if rps <= 0 {
		panic(fmt.Sprintf("tokenbucket.New: rps must be positive, got %v", rps))
	}
	return &Limiter{inner: rate.NewLimiter(rate.Limit(rps), 1)}
}

// Wait blocks until the next call is admitted or ctx ends.
func (l *Limiter) Wait(ctx context.Context) error {
	return l.inner.Wait(ctx)
}
