// Package service holds the router: the core's only service. It decides
// which provider answers a lookup, inside the rules that make the proxy safe
// to point Dawarich at: cache first, pace every provider, respect daily
// quotas, fail over on error, and never answer empty on failure.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Provider bundles one upstream geocoder with its limits.
type Provider struct {
	Geocoder port.Geocoder
	// Limiter paces calls; nil means unpaced.
	Limiter port.Limiter
	// DailyQuota caps calls per UTC day; zero means unlimited.
	DailyQuota int
}

type runtimeProvider struct {
	Provider
	quota *quotaCounter
}

// Router implements port.GeocodeService over a cache and an ordered list of
// providers. The zero Router is not usable; build it with New.
type Router struct {
	cache    port.Cache
	metrics  port.Metrics
	now      func() time.Time
	maxReuse float64

	providers []runtimeProvider
}

// Option customises a Router.
type Option func(*Router)

// WithMaxReuseDistance sets how close a request must be to a cached answer's
// own coordinates for the answer to be reused, in metres. Only answers with a
// house number get the distance check; street-level answers and genuine
// "nothing here" answers are reused cell-wide. The default is 25 m, roughly
// GPS noise plus one house plot.
func WithMaxReuseDistance(metres float64) Option {
	return func(r *Router) { r.maxReuse = metres }
}

// WithClock overrides the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(r *Router) { r.now = now }
}

// Compile-time guard: the router is the incoming port's implementation.
var _ port.GeocodeService = (*Router)(nil)

// New wires the router. Providers are tried in order.
func New(cache port.Cache, metrics port.Metrics, providers []Provider, opts ...Option) *Router {
	r := &Router{
		cache:    cache,
		metrics:  metrics,
		now:      time.Now,
		maxReuse: 25,
	}
	for _, p := range providers {
		var quota *quotaCounter
		if p.DailyQuota > 0 {
			quota = newQuotaCounter(p.DailyQuota)
		}
		r.providers = append(r.providers, runtimeProvider{Provider: p, quota: quota})
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Reverse resolves the address nearest to the query point: cache first, then
// providers in order. See the package comment for the rules.
func (r *Router) Reverse(ctx context.Context, q domain.ReverseQuery) (domain.Result, error) {
	key := "r:" + q.Coordinates.CellKey()

	if entry, ok := r.cache.Get(key); ok {
		if reusable(entry.Result, q.Coordinates, r.maxReuse) {
			r.metrics.CacheLookup(port.CacheHit)
			return entry.Result, nil
		}
		r.metrics.CacheLookup(port.CacheStale)
	} else {
		r.metrics.CacheLookup(port.CacheMiss)
	}

	result, err := callProviders(ctx, r, func(g port.Geocoder) (domain.Result, bool, error) {
		result, err := g.Reverse(ctx, q)
		return result, result.Found, err
	})
	if err != nil {
		return domain.Result{}, err
	}

	r.cache.Set(key, port.Entry{Result: result, StoredAt: r.now()})
	return result, nil
}

// Forward resolves places matching free text. Cached by normalised query
// text; there is no distance rule because forward answers carry no single
// coordinate to measure against.
func (r *Router) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	key := "f:" + strings.ToLower(strings.TrimSpace(q.Text))

	if entry, ok := r.cache.Get(key); ok {
		r.metrics.CacheLookup(port.CacheHit)
		return entry.Results, nil
	}
	r.metrics.CacheLookup(port.CacheMiss)

	results, err := callProviders(ctx, r, func(g port.Geocoder) ([]domain.Result, bool, error) {
		results, err := g.Forward(ctx, q)
		return results, true, err
	})
	if err != nil {
		return nil, err
	}

	r.cache.Set(key, port.Entry{Results: results, StoredAt: r.now()})
	return results, nil
}

// reusable applies the cache reuse rule: a "nothing here" answer and a
// street-level answer are safe cell-wide; a house-numbered answer is only
// safe near where that house actually is.
func reusable(result domain.Result, q domain.Coordinates, maxReuse float64) bool {
	if !result.Found || !result.Address.HasHouseNumber() {
		return true
	}
	return q.DistanceTo(result.Coordinates) <= maxReuse
}

// callProviders tries each provider in order until one answers. It is the
// home of the never-blank rule: provider errors are collected and surfaced as
// ErrAllProvidersFailed, never converted into an empty answer. found reports
// whether the provider answered at all (always true on success; a genuine
// "nothing here" is still an answer). Free function rather than a method
// because Go methods cannot be generic.
func callProviders[T any](ctx context.Context, r *Router, lookup func(port.Geocoder) (T, bool, error)) (T, error) {
	var zero T
	if len(r.providers) == 0 {
		return zero, domain.ErrNoProviders
	}

	var errs []error
	for _, p := range r.providers {
		name := p.Geocoder.Name()

		if p.quota != nil && p.quota.remaining(r.now()) == 0 {
			r.metrics.ProviderRequest(name, port.ProviderQuotaExhausted, 0)
			continue
		}

		if p.Limiter != nil {
			if err := p.Limiter.Wait(ctx); err != nil {
				// The caller's own deadline is not a provider failure.
				if ctx.Err() != nil {
					return zero, ctx.Err()
				}
				r.metrics.ProviderRequest(name, port.ProviderPacingLimited, 0)
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
		}

		if p.quota != nil && !p.quota.admit(r.now()) {
			r.metrics.ProviderRequest(name, port.ProviderQuotaExhausted, 0)
			continue
		}

		start := r.now()
		result, found, err := lookup(p.Geocoder)
		d := r.now().Sub(start)

		if err != nil {
			r.metrics.ProviderRequest(name, port.ProviderError, d)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if p.quota != nil {
			r.metrics.QuotaRemaining(name, p.quota.remaining(r.now()))
		}
		if found {
			r.metrics.ProviderRequest(name, port.ProviderSuccess, d)
		} else {
			r.metrics.ProviderRequest(name, port.ProviderEmpty, d)
		}
		return result, nil
	}

	if len(errs) > 0 {
		return zero, fmt.Errorf("%w: %w", domain.ErrAllProvidersFailed, errors.Join(errs...))
	}
	// Every provider was quota-exhausted: not an upstream failure, but the
	// caller still cannot get an answer today.
	return zero, domain.ErrAllProvidersFailed
}
