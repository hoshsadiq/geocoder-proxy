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
	err   error
	block bool
}

func (l fakeLimiter) Wait(ctx context.Context) error {
	if l.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return l.err
}

var errBoom = errors.New("upstream exploded")

func house(lat, lon float64) domain.Result {
	return domain.Result{
		Address:     domain.Address{HouseNumber: "12", Street: "Main Street"},
		Coordinates: coords(lat, lon),
	}
}

func street(lat, lon float64) domain.Result {
	return domain.Result{
		Address:     domain.Address{Street: "Main Street"},
		Coordinates: coords(lat, lon),
	}
}

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

// cellNear sits 5.7 m from the answer (inside the 25 m reuse distance);
// cellFar sits 33.4 m away in the same cell (outside it); otherCell is a
// different cell entirely.
var (
	cellAnswer = coords(51.47740, -0.06100)
	cellNear   = coords(51.47745, -0.06098)
	cellFar    = coords(51.47770, -0.06100)
	otherCell  = coords(51.47830, -0.06140)
)

// coords builds valid Coordinates for the fixtures; the literals are all in
// range, so a failure here is a bug in the fixture, not the code.
func coords(lat, lon float64) domain.Coordinates {
	c, err := domain.NewCoordinates(lat, lon)
	if err != nil {
		panic(err)
	}
	return c
}

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

func TestReverseCacheReuse(t *testing.T) {
	houseAnswer := house(cellAnswer.Lat(), cellAnswer.Lon())
	streetAnswer := street(cellAnswer.Lat(), cellAnswer.Lon())
	multi := []domain.Result{
		house(cellAnswer.Lat(), cellAnswer.Lon()),
		{Address: domain.Address{Name: "Corner Shop", OsmID: 123, OsmType: "N", OsmKey: "shop", OsmValue: "convenience"}},
	}

	tests := []struct {
		name         string
		seed         []domain.Result // pre-seeded under the query's key; nil means cold cache
		upstream     []domain.Result // what the provider answers when called
		query        domain.Coordinates
		calls        int // sequential Reverse calls
		wantLen      int
		wantUpstream int
		wantSets     int // cache writes after seeding
		wantOutcomes []port.CacheOutcome
		wantName     string // spot-check the last result set's notable field
	}{
		{
			name:         "hit near the answer skips upstream",
			seed:         []domain.Result{houseAnswer},
			upstream:     []domain.Result{houseAnswer},
			query:        cellNear,
			calls:        1,
			wantLen:      1,
			wantUpstream: 0,
			wantSets:     0,
			wantOutcomes: []port.CacheOutcome{port.CacheHit},
		},
		{
			name:         "stale entry past the reuse distance re-queries",
			seed:         []domain.Result{houseAnswer},
			upstream:     []domain.Result{house(cellFar.Lat(), cellFar.Lon())},
			query:        cellFar,
			calls:        1,
			wantLen:      1,
			wantUpstream: 1,
			wantSets:     1,
			wantOutcomes: []port.CacheOutcome{port.CacheStale},
		},
		{
			name:         "street-level answer is reused across the cell",
			seed:         []domain.Result{streetAnswer},
			upstream:     []domain.Result{streetAnswer},
			query:        cellFar,
			calls:        1,
			wantLen:      1,
			wantUpstream: 0,
			wantSets:     0,
			wantOutcomes: []port.CacheOutcome{port.CacheHit},
		},
		{
			name:         "genuine nothing-here answer is cached",
			seed:         nil,
			upstream:     nil,
			query:        cellNear,
			calls:        2,
			wantLen:      0,
			wantUpstream: 1,
			wantSets:     1,
			wantOutcomes: []port.CacheOutcome{port.CacheMiss, port.CacheHit},
		},
		{
			name:         "multi-result answer set round-trips through the cache",
			seed:         nil,
			upstream:     multi,
			query:        cellNear,
			calls:        2,
			wantLen:      2,
			wantUpstream: 1,
			wantSets:     1,
			wantOutcomes: []port.CacheOutcome{port.CacheMiss, port.CacheHit},
			wantName:     "Corner Shop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cache, metrics := newFakeCache(), &fakeMetrics{}
			geo := reverseGeocoder("p1", tt.upstream, nil)
			if tt.seed != nil {
				cache.Set(reverseKey(query(tt.query)), port.Entry{Results: tt.seed})
				cache.sets = 0
			}
			r := newRouter(cache, metrics, Provider{Geocoder: geo})

			var got []domain.Result
			var err error
			for range tt.calls {
				got, err = r.Reverse(t.Context(), query(tt.query))
				must.NoError(err)
			}
			must.Len(got, tt.wantLen)
			is.Equal(tt.wantUpstream, geo.reverseCalls)
			is.Equal(tt.wantSets, cache.sets)
			is.Equal(tt.wantOutcomes, metrics.cacheOutcomes)
			if tt.wantName != "" {
				is.Equal(tt.wantName, got[len(got)-1].Address.Name)
			}
		})
	}
}

func TestReverseFailures(t *testing.T) {
	tests := []struct {
		name         string
		providers    []Provider
		wantErr      error
		wantCalls    []int // upstream calls per provider, in order
		wantSets     int
		wantOutcomes []port.ProviderOutcome
	}{
		{
			name: "all providers failing is an error, never an empty answer",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", nil, errBoom)},
				{Geocoder: reverseGeocoder("p2", nil, errBoom)},
			},
			wantErr:      domain.ErrAllProvidersFailed,
			wantCalls:    []int{1, 1},
			wantSets:     0,
			wantOutcomes: []port.ProviderOutcome{port.ProviderError, port.ProviderError},
		},
		{
			name: "failover to the second provider",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", nil, errBoom)},
				{Geocoder: reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)},
			},
			wantErr:      nil,
			wantCalls:    []int{1, 1},
			wantSets:     1,
			wantOutcomes: []port.ProviderOutcome{port.ProviderError, port.ProviderSuccess},
		},
		{
			name:         "no providers configured",
			providers:    nil,
			wantErr:      domain.ErrNoProviders,
			wantCalls:    nil,
			wantSets:     0,
			wantOutcomes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cache, metrics := newFakeCache(), &fakeMetrics{}
			r := newRouter(cache, metrics, tt.providers...)

			got, err := r.Reverse(t.Context(), query(cellNear))
			if tt.wantErr != nil {
				must.Error(err)
				must.ErrorIs(err, tt.wantErr)
				is.Nil(got, "a failure must not masquerade as an answer")
			} else {
				must.NoError(err)
				must.NotEmpty(got)
			}
			for i, p := range tt.providers {
				is.Equal(tt.wantCalls[i], p.Geocoder.(*fakeGeocoder).reverseCalls, "provider %d", i)
			}
			is.Equal(tt.wantSets, cache.sets, "failures are never cached")
			is.Equal(tt.wantOutcomes, outcomesOf(metrics))
		})
	}
}

func TestReverseKeys(t *testing.T) {
	is := assert.New(t)
	base := query(cellNear)
	// The exact formats are pinned so a key-format change cannot slip past
	// the option-separation checks.
	is.Equal("r:"+cellNear.CellKey()+":1:0:false", reverseKey(base))
	is.Equal("f:0:main street", forwardKey(domain.ForwardQuery{Text: " Main STREET "}))

	is.NotEqual(reverseKey(base), reverseKey(domain.ReverseQuery{Coordinates: cellNear, Limit: 10, Radius: 1, DistanceSort: true}))
	// Reverse Limit 0 is the provider default (one result) and shares a key
	// with explicit Limit 1.
	is.Equal(reverseKey(base), reverseKey(domain.ReverseQuery{Coordinates: cellNear, Limit: 1}))
	// Forward Limit 0 is the provider default (15 results) and must not
	// share a key with explicit Limit 1.
	is.NotEqual(forwardKey(domain.ForwardQuery{Text: "x"}), forwardKey(domain.ForwardQuery{Text: "x", Limit: 1}))
}

func TestReverseQuota(t *testing.T) {
	tests := []struct {
		name          string
		providers     []Provider
		wantCalls     []int
		wantSecondErr error
	}{
		{
			name: "exhaustion falls over to the next provider",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil), DailyQuota: 1},
				{Geocoder: reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)},
			},
			wantCalls:     []int{1, 1},
			wantSecondErr: nil,
		},
		{
			name: "all quotas exhausted is an error",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil), DailyQuota: 1},
			},
			wantCalls:     []int{1},
			wantSecondErr: domain.ErrAllProvidersFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cache, metrics := newFakeCache(), &fakeMetrics{}
			r := newRouter(cache, metrics, tt.providers...)

			// Two distinct cells, so the cache cannot serve the second call.
			_, err := r.Reverse(t.Context(), query(cellNear))
			must.NoError(err)
			_, err = r.Reverse(t.Context(), query(otherCell))
			if tt.wantSecondErr != nil {
				must.Error(err)
				must.ErrorIs(err, tt.wantSecondErr)
			} else {
				must.NoError(err)
			}
			for i, p := range tt.providers {
				is.Equal(tt.wantCalls[i], p.Geocoder.(*fakeGeocoder).reverseCalls, "provider %d", i)
			}
			is.Contains(outcomesOf(metrics), port.ProviderQuotaExhausted)
		})
	}
}

func TestReversePacingFailureMovesOn(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	p1 := reverseGeocoder("p1", nil, nil)
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)
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
	p1 := reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)
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
	p2 := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)
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

func TestForward(t *testing.T) {
	answer := []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}

	tests := []struct {
		name          string
		queries       []domain.ForwardQuery // sequential calls through one router
		upstream      []domain.Result
		upstreamErr   error
		wantCalls     int
		wantLen       int
		wantErr       error
		wantOutcomes  []port.CacheOutcome
		wantMetricOut port.ProviderOutcome
	}{
		{
			name: "normalised text hits the cache",
			queries: []domain.ForwardQuery{
				{Text: "  MAIN Street ", Limit: 1},
				{Text: "main street", Limit: 1},
			},
			upstream:     answer,
			wantCalls:    1,
			wantLen:      1,
			wantOutcomes: []port.CacheOutcome{port.CacheMiss, port.CacheHit},
		},
		{
			name: "different limits do not share an entry",
			queries: []domain.ForwardQuery{
				{Text: "main street", Limit: 1},
				{Text: "main street", Limit: 3},
			},
			upstream:     answer,
			wantCalls:    2,
			wantLen:      1,
			wantOutcomes: []port.CacheOutcome{port.CacheMiss, port.CacheMiss},
		},
		{
			name: "genuine empty answer is cached and recorded as empty",
			queries: []domain.ForwardQuery{
				{Text: "nowhere"},
				{Text: "NOWHERE"},
			},
			upstream:      nil,
			wantCalls:     1,
			wantLen:       0,
			wantOutcomes:  []port.CacheOutcome{port.CacheMiss, port.CacheHit},
			wantMetricOut: port.ProviderEmpty,
		},
		{
			name: "failover to the second provider",
			queries: []domain.ForwardQuery{
				{Text: "main street"},
			},
			upstream:      answer,
			upstreamErr:   errBoom,
			wantCalls:     1,
			wantLen:       1,
			wantOutcomes:  []port.CacheOutcome{port.CacheMiss},
			wantMetricOut: port.ProviderError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			metrics := &fakeMetrics{}
			p1 := reverseGeocoder("p1", tt.upstream, tt.upstreamErr)
			p2 := reverseGeocoder("p2", answer, nil)
			r := newRouter(newFakeCache(), metrics, Provider{Geocoder: p1}, Provider{Geocoder: p2})

			var got []domain.Result
			var err error
			for _, q := range tt.queries {
				got, err = r.Forward(t.Context(), q)
			}
			if tt.wantErr != nil {
				must.ErrorIs(err, tt.wantErr)
				return
			}
			must.NoError(err)
			must.Len(got, tt.wantLen)
			if tt.upstreamErr == nil {
				is.Equal(tt.wantCalls, p1.forwardCalls)
			} else {
				is.Equal(1, p1.forwardCalls)
				is.Equal(tt.wantCalls, p2.forwardCalls)
			}
			is.Equal(tt.wantOutcomes, metrics.cacheOutcomes)
			if tt.wantMetricOut != "" {
				is.Contains(outcomesOf(metrics), tt.wantMetricOut)
			}
		})
	}
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

func TestNewPanicsOnNilDependencies(t *testing.T) {
	must := require.New(t)
	must.Panics(func() { New(nil, &fakeMetrics{}, nil) })
	must.Panics(func() { New(newFakeCache(), nil, nil) })
	must.Panics(func() { New(newFakeCache(), &fakeMetrics{}, []Provider{{}}) })
}
