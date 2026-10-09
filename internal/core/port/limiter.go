package port

import "context"

// Limiter is the outgoing port for per-provider rate pacing. Implementations
// (the token bucket adapter) wrap a rate limiter; the core stays free of any
// pacing library.
type Limiter interface {
	// Wait blocks until the next call is admitted or the context ends.
	// Returning an error means "not admitted"; the router moves to the next
	// provider.
	Wait(ctx context.Context) error
}
