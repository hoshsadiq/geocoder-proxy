package port

import "time"

// CacheOutcome classifies a cache lookup for metrics.
type CacheOutcome string

const (
	// CacheHit: a reusable entry was found.
	CacheHit CacheOutcome = "hit"
	// CacheMiss: no entry existed for the key.
	CacheMiss CacheOutcome = "miss"
	// CacheStale: an entry existed but the reuse rule rejected it (too far
	// from the answer it holds), so an upstream call happens anyway.
	CacheStale CacheOutcome = "stale"
)

// ProviderOutcome classifies one upstream attempt for metrics.
type ProviderOutcome string

const (
	// ProviderSuccess: the provider answered with a found result.
	ProviderSuccess ProviderOutcome = "success"
	// ProviderEmpty: the provider genuinely answered "nothing here".
	ProviderEmpty ProviderOutcome = "empty"
	// ProviderError: the provider call failed and the router moved on.
	ProviderError ProviderOutcome = "error"
	// ProviderQuotaExhausted: the provider was skipped for the rest of the
	// UTC day.
	ProviderQuotaExhausted ProviderOutcome = "quota_exhausted"
	// ProviderPacingLimited: the rate limiter could not admit the call
	// inside the caller's context, so the router moved on.
	ProviderPacingLimited ProviderOutcome = "pacing_limited"
)

// Metrics is the outgoing port for operational telemetry. Implementations
// must keep label cardinality bounded: provider names and the outcome
// constants above only, never coordinates or query text.
type Metrics interface {
	// CacheLookup records one cache lookup with its outcome.
	CacheLookup(outcome CacheOutcome)

	// ProviderRequest records one upstream attempt. d is the call duration
	// and is meaningless for outcomes that never reached the network.
	ProviderRequest(provider string, outcome ProviderOutcome, d time.Duration)

	// QuotaRemaining reports how many calls a provider has left today; it is
	// only called for providers with a configured daily quota.
	QuotaRemaining(provider string, remaining int)
}
