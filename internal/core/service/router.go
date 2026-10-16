// Package service holds the router: the core's only service. It decides
// which provider answers a lookup, inside the rules that make the proxy safe
// to point Dawarich at: cache first, pace every provider, respect daily
// quotas, fail over on error, and never answer empty on failure.
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// maxForwardTextBytes caps the forward query text, mostly to bound the size
// of cache keys, which are caller-derived.
const maxForwardTextBytes = 512

// Provider bundles one upstream geocoder with its limits.
type Provider struct {
	// Name is the configured instance name. Metrics and logs use it, so two
	// instances of one provider type stay distinguishable. Empty falls back to
	// the geocoder's own name.
	Name string
	// Tier groups providers for rotation: a lookup rotates only among the
	// providers of the earliest tier that has anyone available, and a later
	// tier is reached only when every provider before it failed or is out of
	// quota. Providers sharing a tier rotate among themselves; a provider
	// alone in its tier keeps its strict turn. Zero is treated as the first
	// tier.
	Tier     int
	Geocoder port.Geocoder
	// Limiter paces calls; nil means unpaced.
	Limiter port.Limiter
	// DailyQuota caps calls over a rolling 24-hour window; zero means
	// unlimited.
	DailyQuota int
}

// InstanceName is the label used in logs and metrics: the configured name,
// falling back to the provider's own type name when Name is empty.
func (p Provider) InstanceName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Geocoder.Name()
}

type runtimeProvider struct {
	Provider
	// instance is the name that labels metrics and logs; it is Name when set,
	// otherwise the geocoder's own name.
	instance string
	// tier is the grouping used for rotation; 1 or more, first tier first.
	tier  int
	quota *quotaCounter
}

// Router implements port.GeocodeService over a cache and providers grouped into
// tiers. The zero Router is not usable; build it with New.
type Router struct {
	cache          port.Cache
	metrics        port.Metrics
	now            func() time.Time
	maxReuse       float64
	attemptTimeout time.Duration

	providers []runtimeProvider
	// cursor advances once per lookup that reaches the providers; it is the
	// rotation offset within each tier.
	cursor atomic.Uint64
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

// WithAttemptTimeout caps one provider attempt, so a slow provider fails over
// to the next instead of consuming the whole request budget. Zero disables the
// per-attempt cap. A negative value is a programming error and panics, matching
// the other constructors.
func WithAttemptTimeout(d time.Duration) Option {
	if d < 0 {
		panic("service.WithAttemptTimeout: negative timeout")
	}
	return func(r *Router) { r.attemptTimeout = d }
}

// Compile-time guard: the router is the incoming port's implementation.
var _ port.GeocodeService = (*Router)(nil)

// New wires the router. Providers rotate within their tier, earliest tier
// first; a provider alone in its tier is therefore always tried before the next
// tier. Misconfiguration is a startup failure, so New panics on nil dependencies
// rather than letting the first request crash.
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
		instance := p.InstanceName()
		var quota *quotaCounter
		if p.DailyQuota > 0 {
			quota = newQuotaCounter(p.DailyQuota)
		}
		r.providers = append(r.providers, runtimeProvider{
			Provider: p,
			instance: instance,
			// The loader is what turns "no tier" into a tier of the entry's own;
			// a caller that omits Tier lands in the first tier here instead, so
			// it must set Tier itself to keep its providers apart.
			tier:  max(p.Tier, 1),
			quota: quota,
		})
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// orderedProviders returns the provider order for one lookup: grouped by tier,
// earliest first, ring-rotated within each tier so successive lookups start at
// a different provider. Rotation only shares a lookup across providers of the
// same tier, and a later tier is reached only after every provider before it
// failed or ran out of quota, so a rescue provider never takes a steady share
// of the traffic.
func (r *Router) orderedProviders() []runtimeProvider {
	if len(r.providers) < 2 {
		return r.providers
	}

	// One lookup consumes one cursor value. Tiers hold different numbers of
	// providers, so the offset is reduced modulo each tier's size; the modulus
	// stays in uint64 to keep it in range when the counter wraps.
	offset := r.cursor.Add(1) - 1

	tiers := make([]int, 0, len(r.providers))
	seen := make(map[int]bool, len(r.providers))
	for _, p := range r.providers {
		if !seen[p.tier] {
			seen[p.tier] = true
			tiers = append(tiers, p.tier)
		}
	}
	slices.Sort(tiers)

	ordered := make([]runtimeProvider, 0, len(r.providers))
	for _, tier := range tiers {
		members := make([]int, 0, len(r.providers))
		for i, p := range r.providers {
			if p.tier == tier {
				members = append(members, i)
			}
		}
		start := int(offset % uint64(len(members)))
		for k := range members {
			member := members[(start+k)%len(members)]
			ordered = append(ordered, r.providers[member])
		}
	}
	return ordered
}

// Reverse resolves the places nearest to the query point: cache first, then
// providers in order. See the package comment for the rules.
func (r *Router) Reverse(ctx context.Context, q domain.ReverseQuery) (results []domain.Result, err error) {
	defer func() { r.recordLookup(results, err) }()
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

	results, err = callProviders(ctx, r, func(ctx context.Context, g port.Geocoder) ([]domain.Result, bool, error) {
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
func (r *Router) Forward(ctx context.Context, q domain.ForwardQuery) (results []domain.Result, err error) {
	defer func() { r.recordLookup(results, err) }()
	if len(q.Text) > maxForwardTextBytes {
		return nil, fmt.Errorf("%w: forward text exceeds %d bytes", domain.ErrInvalidQuery, maxForwardTextBytes)
	}
	key := forwardKey(q)

	if entry, ok := r.cache.Get(key); ok {
		r.metrics.CacheLookup(port.CacheHit)
		return entry.Results, nil
	}
	r.metrics.CacheLookup(port.CacheMiss)

	results, err = callProviders(ctx, r, func(ctx context.Context, g port.Geocoder) ([]domain.Result, bool, error) {
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

// recordLookup counts what the lookup returned to its caller. A caller who hung
// up before an answer existed is not counted: the service did not fail, and the
// HTTP layer writes nothing for it.
func (r *Router) recordLookup(results []domain.Result, err error) {
	switch {
	case err == nil && len(results) > 0:
		r.metrics.Lookup(port.LookupOK)
	case err == nil:
		r.metrics.Lookup(port.LookupEmpty)
	case errors.Is(err, context.Canceled):
	default:
		r.metrics.Lookup(port.LookupFailed)
	}
}

// attemptContext bounds one provider call; zero disables the cap and returns
// the caller's context unchanged.
func (r *Router) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.attemptTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, r.attemptTimeout)
}

// budgetCut reports whether the request context ended the lookup mid-attempt. A
// deadline cut (the budget, or a pace wait) is recorded as a provider error so the
// error-rate alert sees an unanswered lookup; plain cancellation (the caller hung
// up) is not a provider failure and is recorded as nothing. d is the attempt's
// elapsed time, or zero when the pace wait was cut before any attempt began.
func (r *Router) budgetCut(ctx context.Context, provider string, d time.Duration) (bool, error) {
	err := ctx.Err()
	if err == nil {
		return false, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		r.metrics.ProviderRequest(provider, port.ProviderError, d)
	}
	return true, err
}

// callProviders tries each provider in order until one answers. It is the
// home of the never-blank rule: provider errors are collected and surfaced
// as ErrAllProvidersFailed, never converted into an empty answer. found only
// drives the success-vs-empty metric outcome; an empty answer is still an
// answer. Free function rather than a method because Go methods cannot be
// generic.
func callProviders[T any](ctx context.Context, r *Router, lookup func(context.Context, port.Geocoder) (T, bool, error)) (T, error) {
	var zero T
	providers := r.orderedProviders()
	if len(providers) == 0 {
		return zero, domain.ErrNoProviders
	}

	var errs []error
	for _, p := range providers {
		// A caller whose own context has ended is never a provider failure,
		// and must not spend quota on the remaining providers.
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		name := p.instance

		if p.quota != nil && p.quota.remaining(r.now()) == 0 {
			r.metrics.ProviderRequest(name, port.ProviderQuotaExhausted, 0)
			continue
		}

		if p.Limiter != nil {
			if err := p.Limiter.Wait(ctx); err != nil {
				if done, doneErr := r.budgetCut(ctx, name, 0); done {
					return zero, doneErr
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
		attemptCtx, cancel := r.attemptContext(ctx)
		result, found, err := lookup(attemptCtx, p.Geocoder)
		cancel()
		d := r.now().Sub(start)
		if p.quota != nil {
			r.metrics.QuotaRemaining(name, p.quota.remaining(r.now()))
		}

		if err != nil {
			if done, doneErr := r.budgetCut(ctx, name, d); done {
				return zero, doneErr
			}
			if errors.Is(err, domain.ErrProviderTerminal) {
				r.metrics.ProviderTerminal(name)
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
