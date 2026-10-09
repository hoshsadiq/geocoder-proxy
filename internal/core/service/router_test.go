package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// --- fakes -----------------------------------------------------------------

type fakeGeocoder struct {
	name         string
	reverseFn    func(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error)
	forwardFn    func(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error)
	reverseCalls int
	forwardCalls int
}

func (f *fakeGeocoder) Name() string { return f.name }

func (f *fakeGeocoder) Reverse(ctx context.Context, q domain.ReverseQuery) ([]domain.Result, error) {
	f.reverseCalls++
	return f.reverseFn(ctx, q)
}

func (f *fakeGeocoder) Forward(ctx context.Context, q domain.ForwardQuery) ([]domain.Result, error) {
	f.forwardCalls++
	return f.forwardFn(ctx, q)
}

type fakeCache struct {
	mu      sync.Mutex
	entries map[string]port.Entry
	sets    int
}

func newFakeCache() *fakeCache {
	return &fakeCache{entries: make(map[string]port.Entry)}
}

func (c *fakeCache) Get(key string) (port.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return e, ok
}

func (c *fakeCache) Set(key string, entry port.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	c.entries[key] = entry
}

type providerCall struct {
	name    string
	outcome port.ProviderOutcome
}

type fakeMetrics struct {
	cacheOutcomes []port.CacheOutcome
	calls         []providerCall
}

func (m *fakeMetrics) CacheLookup(outcome port.CacheOutcome) {
	m.cacheOutcomes = append(m.cacheOutcomes, outcome)
}

func (m *fakeMetrics) ProviderRequest(name string, outcome port.ProviderOutcome, _ time.Duration) {
	m.calls = append(m.calls, providerCall{name: name, outcome: outcome})
}

func (m *fakeMetrics) QuotaRemaining(string, int) {}

type fakeLimiter struct {
	// err is returned immediately when set.
	err error
	// block waits for the context to end, for the cancellation test.
	block bool
}

func (l fakeLimiter) Wait(ctx context.Context) error {
	if l.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return l.err
}

// --- fixtures --------------------------------------------------------------

var errBoom = errors.New("upstream exploded")

// house answers with a house number; street answers street-level only.
func house(lat, lon float64) domain.Result {
	return domain.Result{
		Address:     domain.Address{HouseNumber: "12", Street: "Main Street"},
		Coordinates: domain.Coordinates{Lat: lat, Lon: lon},
	}
}

func street(lat, lon float64) domain.Result {
	return domain.Result{
		Address:     domain.Address{Street: "Main Street"},
		Coordinates: domain.Coordinates{Lat: lat, Lon: lon},
	}
}

// reverseGeocoder answers reverse and forward with the same fixed list.
func reverseGeocoder(name string, results []domain.Result, err error) *fakeGeocoder {
	return &fakeGeocoder{
		name: name,
		reverseFn: func(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
			return results, err
		},
		forwardFn: func(context.Context, domain.ForwardQuery) ([]domain.Result, error) {
			return results, err
		},
	}
}

func newRouter(cache *fakeCache, metrics *fakeMetrics, providers ...Provider) *Router {
	return New(cache, metrics, providers, WithClock(func() time.Time {
		return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	}))
}

// The fixtures below share one cache cell (~55 m grid at 0.0005°). cellNear
// sits 5.7 m from the answer (inside the 25 m reuse distance); cellFar sits
// 33.4 m away in the same cell (outside it).
var (
	cellAnswer = domain.Coordinates{Lat: 51.47740, Lon: -0.06100}
	cellNear   = domain.Coordinates{Lat: 51.47745, Lon: -0.06098}
	cellFar    = domain.Coordinates{Lat: 51.47770, Lon: -0.06100}
)

func query(c domain.Coordinates) domain.ReverseQuery {
	return domain.ReverseQuery{Coordinates: c}
}

func outcomesOf(m *fakeMetrics) []port.ProviderOutcome {
	var outcomes []port.ProviderOutcome
	for _, c := range m.calls {
		outcomes = append(outcomes, c.outcome)
	}
	return outcomes
}

// --- reverse: cache behaviour ----------------------------------------------

func TestReverseCacheHitNearAnswer(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	geo := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)

	cache.Set(reverseKey(query(cellNear)), port.Entry{Results: []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}})
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(0, geo.reverseCalls, "a cached answer within the reuse distance must skip upstream")
	is.Equal([]port.CacheOutcome{port.CacheHit}, metrics.cacheOutcomes)
}

func TestReverseStaleEntryCallsUpstream(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	geo := reverseGeocoder("p1", []domain.Result{house(cellFar.Lat, cellFar.Lon)}, nil)

	// Cached house-numbered answer sits 33.4 m from the request: too far to
	// reuse, same cell.
	cache.Set(reverseKey(query(cellFar)), port.Entry{Results: []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}})
	cache.sets = 0
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Reverse(t.Context(), query(cellFar))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, geo.reverseCalls, "a cached answer past the reuse distance must not be reused")
	is.Equal([]port.CacheOutcome{port.CacheStale}, metrics.cacheOutcomes)
	is.Equal(1, cache.sets, "the fresh answer replaces the stale one")
}

func TestReverseStreetLevelReusedAcrossCell(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	geo := reverseGeocoder("p1", []domain.Result{street(cellAnswer.Lat, cellAnswer.Lon)}, nil)

	// Street-level answers carry no house number, so the reuse distance does
	// not apply: a street name does not change inside one cell.
	cache.Set(reverseKey(query(cellFar)), port.Entry{Results: []domain.Result{street(cellAnswer.Lat, cellAnswer.Lon)}})
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Reverse(t.Context(), query(cellFar))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal("Main Street", got[0].Address.Street)
	is.Equal(0, geo.reverseCalls)
}

func TestReverseNegativeAnswerCached(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	geo := reverseGeocoder("p1", nil, nil)
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err, "a genuine 'nothing here' is an answer, not an error")
	is.Empty(got)
	is.Equal(1, geo.reverseCalls)

	got, err = r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	is.Empty(got)
	is.Equal(1, geo.reverseCalls, "the negative answer must be cached")
}

func TestReverseMultiResultRoundTrip(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	results := []domain.Result{
		house(cellAnswer.Lat, cellAnswer.Lon),
		{Address: domain.Address{Name: "Corner Shop", OsmID: 123, OsmType: "N", OsmKey: "shop", OsmValue: "convenience"}},
	}
	geo := reverseGeocoder("p1", results, nil)
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 2)

	got, err = r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 2, "the full answer set must come back from the cache")
	is.Equal("Corner Shop", got[1].Address.Name)
	is.Equal(1, geo.reverseCalls)
}

func TestReverseKeySeparatesOptions(t *testing.T) {
	is := assert.New(t)
	base := query(cellNear)
	// The exact format is pinned so a key-format change cannot slip past the
	// option-separation checks below.
	is.Equal("r:"+cellNear.CellKey()+":1:0:false", reverseKey(base))
	is.NotEqual(reverseKey(base), reverseKey(domain.ReverseQuery{Coordinates: cellNear, Limit: 10, Radius: 1, DistanceSort: true}))
	// Limit 0 is the provider default and shares a key with explicit Limit 1.
	is.Equal(reverseKey(base), reverseKey(domain.ReverseQuery{Coordinates: cellNear, Limit: 1}))
}

// --- reverse: the never-blank rule -----------------------------------------

func TestReverseAllProvidersFailIsAnError(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	p1 := reverseGeocoder("p1", nil, errBoom)
	p2 := reverseGeocoder("p2", nil, errBoom)
	r := newRouter(cache, metrics, Provider{Geocoder: p1}, Provider{Geocoder: p2})

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.Error(err)
	must.ErrorIs(err, domain.ErrAllProvidersFailed)
	is.Nil(got, "a total failure must not masquerade as an empty answer")
	is.Equal(0, cache.sets, "failures are never cached")
	is.Equal([]port.ProviderOutcome{port.ProviderError, port.ProviderError}, outcomesOf(metrics))
}

func TestReverseFailoverToSecondProvider(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	p1 := reverseGeocoder("p1", nil, errBoom)
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(cache, metrics, Provider{Geocoder: p1}, Provider{Geocoder: p2})

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, p1.reverseCalls)
	is.Equal(1, p2.reverseCalls)
}

func TestReverseNoProviders(t *testing.T) {
	must := require.New(t)
	r := newRouter(newFakeCache(), &fakeMetrics{})

	_, err := r.Reverse(t.Context(), query(cellNear))
	must.Error(err)
	must.ErrorIs(err, domain.ErrNoProviders)
}

// --- quotas and pacing ------------------------------------------------------

func TestReverseQuotaExhaustionFallsOver(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	p1 := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(cache, metrics,
		Provider{Geocoder: p1, DailyQuota: 1},
		Provider{Geocoder: p2},
	)

	// Two distinct cells, so the cache cannot serve the second call.
	_, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	farCell := domain.Coordinates{Lat: 51.47830, Lon: -0.06140}
	_, err = r.Reverse(t.Context(), query(farCell))
	must.NoError(err)

	is.Equal(1, p1.reverseCalls, "p1's quota is one call")
	is.Equal(1, p2.reverseCalls, "the second lookup falls over to p2")
	is.Contains(outcomesOf(metrics), port.ProviderQuotaExhausted)
}

func TestReverseAllQuotasExhaustedIsAnError(t *testing.T) {
	must := require.New(t)
	p1 := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{}, Provider{Geocoder: p1, DailyQuota: 1})

	_, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	_, err = r.Reverse(t.Context(), query(domain.Coordinates{Lat: 51.47830, Lon: -0.06140}))
	must.Error(err)
	must.ErrorIs(err, domain.ErrAllProvidersFailed)
	must.Equal(1, p1.reverseCalls)
}

func TestReversePacingFailureMovesOn(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	p1 := reverseGeocoder("p1", nil, nil)
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{},
		Provider{Geocoder: p1, Limiter: fakeLimiter{err: errors.New("would exceed deadline")}},
		Provider{Geocoder: p2},
	)

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(0, p1.reverseCalls, "a pacing failure skips the provider without calling it")
	is.Equal(1, p2.reverseCalls)
}

func TestReverseContextCancelledDuringPacing(t *testing.T) {
	must := require.New(t)
	p1 := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{},
		Provider{Geocoder: p1, Limiter: fakeLimiter{block: true}},
	)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Reverse(ctx, query(cellNear))
	must.Error(err)
	must.ErrorIs(err, context.DeadlineExceeded)
	must.Equal(0, p1.reverseCalls)
}

func TestReverseContextCancelledDuringProviderCall(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	// p1 blocks until the caller's context ends, then reports that error.
	p1 := &fakeGeocoder{
		name: "p1",
		reverseFn: func(ctx context.Context, _ domain.ReverseQuery) ([]domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), metrics,
		Provider{Geocoder: p1, DailyQuota: 10},
		Provider{Geocoder: p2, DailyQuota: 10},
	)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Reverse(ctx, query(cellNear))
	must.Error(err)
	must.ErrorIs(err, context.DeadlineExceeded)
	is.Equal(0, p2.reverseCalls, "a dead caller must not spend quota on the remaining providers")
	is.NotContains(outcomesOf(metrics), port.ProviderError,
		"a caller-side cancellation is not a provider failure")
}

// --- forward ----------------------------------------------------------------

func TestForwardCachesByNormalisedText(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	cache, metrics := newFakeCache(), &fakeMetrics{}
	geo := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(cache, metrics, Provider{Geocoder: geo})

	got, err := r.Forward(t.Context(), domain.ForwardQuery{Text: "  MAIN Street ", Limit: 1})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, geo.forwardCalls)

	got, err = r.Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 1})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, geo.forwardCalls, "the normalised text key must hit the cache")
	is.Equal([]port.CacheOutcome{port.CacheMiss, port.CacheHit}, metrics.cacheOutcomes)
}

func TestForwardKeySeparatesLimit(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	geo := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{}, Provider{Geocoder: geo})

	_, err := r.Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 1})
	must.NoError(err)
	_, err = r.Forward(t.Context(), domain.ForwardQuery{Text: "main street", Limit: 3})
	must.NoError(err)
	is.Equal(2, geo.forwardCalls, "a wider request must not be served a narrower cached answer")
}

func TestForwardEmptyAnswerCached(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	geo := &fakeGeocoder{
		name: "p1",
		forwardFn: func(context.Context, domain.ForwardQuery) ([]domain.Result, error) {
			return nil, nil
		},
	}
	r := newRouter(newFakeCache(), metrics, Provider{Geocoder: geo})

	got, err := r.Forward(t.Context(), domain.ForwardQuery{Text: "nowhere"})
	must.NoError(err, "a genuine empty answer is not an error")
	is.Empty(got)
	is.Contains(outcomesOf(metrics), port.ProviderEmpty)

	_, err = r.Forward(t.Context(), domain.ForwardQuery{Text: "NOWHERE"})
	must.NoError(err)
	is.Equal(1, geo.forwardCalls, "the empty answer must be cached")
}

func TestForwardOversizedTextRejected(t *testing.T) {
	must := require.New(t)
	geo := reverseGeocoder("p1", nil, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{}, Provider{Geocoder: geo})

	_, err := r.Forward(t.Context(), domain.ForwardQuery{Text: string(make([]byte, maxForwardTextBytes+1))})
	must.Error(err)
	must.ErrorIs(err, domain.ErrInvalidQuery)
	must.Equal(0, geo.forwardCalls)
}

func TestForwardFailover(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	p1 := reverseGeocoder("p1", nil, errBoom)
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat, cellAnswer.Lon)}, nil)
	r := newRouter(newFakeCache(), &fakeMetrics{}, Provider{Geocoder: p1}, Provider{Geocoder: p2})

	got, err := r.Forward(t.Context(), domain.ForwardQuery{Text: "main street"})
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, p1.forwardCalls)
	is.Equal(1, p2.forwardCalls)
}

// --- constructor validation --------------------------------------------------

func TestNewPanicsOnNilDependencies(t *testing.T) {
	must := require.New(t)
	must.Panics(func() { New(nil, &fakeMetrics{}, nil) })
	must.Panics(func() { New(newFakeCache(), nil, nil) })
	must.Panics(func() { New(newFakeCache(), &fakeMetrics{}, []Provider{{}}) })
}
