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

// maxForwardTextBytes caps the forward query text, mostly to bound the size
// of cache keys, which are caller-derived.
const maxForwardTextBytes = 512

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

// New wires the router. Providers are tried in order. Misconfiguration is a
// startup failure, so New panics on nil dependencies rather than letting the
// first request crash.
func New(cache port.Cache, metrics port.Metrics, providers []Provider, opts ...Option) *Router {
	if cache == nil {
		panic("service.New: nil cache")
	}
	if metrics == nil {
		panic("service.New: nil metrics")
	}
	r := &Router{
		cache:    cache,
		metrics:  metrics,
		now:      time.Now,
		maxReuse: 25,
	}
	for _, p := range providers {
		if p.Geocoder == nil {
			panic("service.New: provider with nil geocoder")
		}
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

// Reverse resolves the places nearest to the query point: cache first, then
// providers in order. See the package comment for the rules.
func (r *Router) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	key := reverseKey(q)

	if entry, ok := r.cache.Get(key); ok {
		if reusable(entry.Results, q.Coordinates, r.maxReuse) {
			r.metrics.CacheLookup(port.CacheHit)
			return entry.Results, nil
		}
		r.metrics.CacheLookup(port.CacheStale)
	} else {
		r.metrics.CacheLookup(port.CacheMiss)
	}

	results, err := callProviders(ctx, r, func(g port.Geocoder) ([]domain.Result, bool, error) {
		results, err := g.Reverse(ctx, q)
		return results, len(results) > 0, err
	})
	if err != nil {
		return nil, err
	}

	r.cache.Set(key, port.Entry{Results: results, StoredAt: r.now()})
	return results, nil
}

// reverseKey derives the reverse cache key: the cell, plus the query options
// that change the answer set. A Limit below 1 means the provider default,
// so it normalises to 1 to share one entry with explicit Limit=1 callers.
func reverseKey(q domain.ReverseQuery) string {
	return fmt.Sprintf("r:%s:%d:%g:%t", q.Coordinates.CellKey(), max(q.Limit, 1), q.Radius, q.DistanceSort)
}

// Forward resolves places matching free text. Cached by normalised query
// text plus the limit; there is no distance rule because forward answers
// carry no single coordinate to measure against.
func (r *Router) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	if len(q.Text) > maxForwardTextBytes {
		return nil, fmt.Errorf("%w: forward text exceeds %d bytes", domain.ErrInvalidQuery, maxForwardTextBytes)
	}
	key := forwardKey(q)

	if entry, ok := r.cache.Get(key); ok {
		r.metrics.CacheLookup(port.CacheHit)
		return entry.Results, nil
	}
	r.metrics.CacheLookup(port.CacheMiss)

	results, err := callProviders(ctx, r, func(g port.Geocoder) ([]domain.Result, bool, error) {
		results, err := g.Forward(ctx, q)
		return results, len(results) > 0, err
	})
	if err != nil {
		return nil, err
	}

	r.cache.Set(key, port.Entry{Results: results, StoredAt: r.now()})
	return results, nil
}

func forwardKey(q domain.ForwardQuery) string {
	// No normalisation here, unlike reverseKey: Photon's forward default is
	// 15 results, so Limit 0 (provider default) and Limit 1 are different
	// answers and must not share an entry.
	return fmt.Sprintf("f:%d:%s", q.Limit, strings.ToLower(strings.TrimSpace(q.Text)))
}

// reusable applies the cache reuse rule: a "nothing here" answer and a
// street-level answer are safe cell-wide; a house-numbered answer is only
// safe near where that house actually is. The first result is the nearest
// one, so it speaks for the whole answer set.
func reusable(results []domain.Result, q domain.Coordinates, maxReuse float64) bool {
	if len(results) == 0 || !results[0].Address.HasHouseNumber() {
		return true
	}
	return q.DistanceTo(results[0].Coordinates) <= maxReuse
}

// callProviders tries each provider in order until one answers. It is the
// home of the never-blank rule: provider errors are collected and surfaced
// as ErrAllProvidersFailed, never converted into an empty answer. found only
// drives the success-vs-empty metric outcome; an empty answer is still an
// answer. Free function rather than a method because Go methods cannot be
// generic.
func callProviders[T any](ctx context.Context, r *Router, lookup func(port.Geocoder) (T, bool, error)) (T, error) {
	var zero T
	if len(r.providers) == 0 {
		return zero, domain.ErrNoProviders
	}

	var errs []error
	for _, p := range r.providers {
		// A caller whose own context has ended is never a provider failure,
		// and must not spend quota on the remaining providers.
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		name := p.Geocoder.Name()

		if p.quota != nil && p.quota.remaining(r.now()) == 0 {
			r.metrics.ProviderRequest(name, port.ProviderQuotaExhausted, 0)
			continue
		}

		if p.Limiter != nil {
			if err := p.Limiter.Wait(ctx); err != nil {
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
		if p.quota != nil {
			r.metrics.QuotaRemaining(name, p.quota.remaining(r.now()))
		}

		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			r.metrics.ProviderRequest(name, port.ProviderError, d)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
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
