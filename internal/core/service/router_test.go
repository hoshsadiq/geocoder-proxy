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

type quotaCall struct {
	name      string
	remaining int
}

type fakeMetrics struct {
	cacheOutcomes []port.CacheOutcome
	calls         []providerCall
	lookups       []port.LookupResult
	quotas        []quotaCall
}

func (m *fakeMetrics) CacheLookup(outcome port.CacheOutcome) {
	m.cacheOutcomes = append(m.cacheOutcomes, outcome)
}

func (m *fakeMetrics) ProviderRequest(name string, outcome port.ProviderOutcome, _ time.Duration) {
	m.calls = append(m.calls, providerCall{name: name, outcome: outcome})
}

func (m *fakeMetrics) Lookup(result port.LookupResult) {
	m.lookups = append(m.lookups, result)
}

func (m *fakeMetrics) QuotaRemaining(name string, remaining int) {
	m.quotas = append(m.quotas, quotaCall{name: name, remaining: remaining})
}

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

// countingLimiter admits every call and counts the waits, so a test can see
// that each instance paces through its own limiter.
type countingLimiter struct {
	waits int
}

func (l *countingLimiter) Wait(context.Context) error {
	l.waits++
	return nil
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

// reversingOnContext blocks until the caller's context ends and then reports
// that error, so a test can hold a lookup open for a deadline or a hang-up.
func reversingOnContext(name string) *fakeGeocoder {
	return &fakeGeocoder{
		name: name,
		reverseFn: func(ctx context.Context, _ domain.ReverseQuery) ([]domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

func newRouter(cache *fakeCache, metrics *fakeMetrics, providers ...Provider) *Router {
	return New(cache, metrics, providers, WithClock(func() time.Time {
		return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	}))
}

// cellNear sits 5.7 m from the answer (inside the 25 m reuse distance);
// cellFar sits 33.4 m away in the same cell (outside it); otherCell, cellThird
// and cellFourth are different cells again, so a sequence of lookups can never
// be served from the cache.
var (
	cellAnswer = coords(51.47740, -0.06100)
	cellNear   = coords(51.47745, -0.06098)
	cellFar    = coords(51.47770, -0.06100)
	otherCell  = coords(51.47830, -0.06140)
	cellThird  = coords(51.47900, -0.06000)
	cellFourth = coords(51.48000, -0.05800)
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

// providerNames is the provider label of every recorded attempt, in order, so a
// test can see which instance the router picked.
func providerNames(m *fakeMetrics) []string {
	names := make([]string, 0, len(m.calls))
	for _, c := range m.calls {
		names = append(names, c.name)
	}
	return names
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
	answer := []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}
	tests := []struct {
		name        string
		providers   []Provider
		lookups     []domain.Coordinates // distinct cells, so the cache never serves one
		wantCalls   []int
		wantLastErr error
	}{
		{
			// Rotation starts at p1, then p2, then p1 again; the third lookup
			// finds p1 out of quota and falls over to p2.
			name: "exhaustion falls over to the next provider",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", answer, nil), DailyQuota: 1},
				{Geocoder: reverseGeocoder("p2", answer, nil)},
			},
			lookups:     []domain.Coordinates{cellNear, otherCell, cellThird},
			wantCalls:   []int{1, 2},
			wantLastErr: nil,
		},
		{
			name: "all quotas exhausted is an error",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", answer, nil), DailyQuota: 1},
			},
			lookups:     []domain.Coordinates{cellNear, otherCell},
			wantCalls:   []int{1},
			wantLastErr: domain.ErrAllProvidersFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cache, metrics := newFakeCache(), &fakeMetrics{}
			r := newRouter(cache, metrics, tt.providers...)

			var err error
			for _, cell := range tt.lookups {
				_, err = r.Reverse(t.Context(), query(cell))
			}
			if tt.wantLastErr != nil {
				must.Error(err)
				must.ErrorIs(err, tt.wantLastErr)
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

// A lookup the service could not answer must show up as result=failed even when
// no provider attempt failed: a pacing-exhausted outage leaves every attempt
// pacing_limited and zero error outcomes, which is the blind spot the
// request-level counter exists to close.
func TestLookupResultRecorded(t *testing.T) {
	answer := []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}

	tests := []struct {
		name      string
		providers []Provider
		reverse   func(ctx context.Context, r *Router) error
		want      []port.LookupResult
	}{
		{
			name:      "a result is ok",
			providers: []Provider{{Geocoder: reverseGeocoder("p1", answer, nil)}},
			want:      []port.LookupResult{port.LookupOK},
		},
		{
			name:      "a genuine nothing-here is empty",
			providers: []Provider{{Geocoder: reverseGeocoder("p1", nil, nil)}},
			want:      []port.LookupResult{port.LookupEmpty},
		},
		{
			name:      "provider failure is failed",
			providers: []Provider{{Geocoder: reverseGeocoder("p1", nil, errBoom)}},
			want:      []port.LookupResult{port.LookupFailed},
		},
		{
			// Every provider pacing-limited: no provider outcome is an error,
			// but the caller still got no answer.
			name: "pacing-exhausted outage is failed",
			providers: []Provider{
				{Geocoder: reverseGeocoder("p1", answer, nil), Limiter: fakeLimiter{err: errors.New("would exceed deadline")}},
			},
			want: []port.LookupResult{port.LookupFailed},
		},
		{
			name: "no configured provider is failed",
			want: []port.LookupResult{port.LookupFailed},
		},
		{
			// The budget ended the attempt: the caller got no answer, so the
			// lookup is failed however the provider outcome is recorded.
			name:      "a budget-cut lookup is failed",
			providers: []Provider{{Geocoder: reversingOnContext("p1")}},
			reverse: func(ctx context.Context, r *Router) error {
				cut, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
				_, err := r.Reverse(cut, query(cellNear))
				return err
			},
			want: []port.LookupResult{port.LookupFailed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			metrics := &fakeMetrics{}
			r := newRouter(newFakeCache(), metrics, tt.providers...)

			if tt.reverse != nil {
				_ = tt.reverse(t.Context(), r)
			} else {
				_, _ = r.Reverse(t.Context(), query(cellNear))
			}
			is.Equal(tt.want, metrics.lookups)
		})
	}
}

func TestLookupResultSkipsDepartedCaller(t *testing.T) {
	is := assert.New(t)
	metrics := &fakeMetrics{}
	r := newRouter(newFakeCache(), metrics,
		Provider{Geocoder: reverseGeocoder("p1", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)})

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = r.Reverse(cancelled, query(cellNear))
	is.Empty(metrics.lookups, "a caller who hung up is not the service failing")
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

func TestReverseAttemptTimeoutFailsOver(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	hanging := &fakeGeocoder{
		name: "p1",
		reverseFn: func(ctx context.Context, _ domain.ReverseQuery) ([]domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	healthy := reverseGeocoder("p2", []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)
	r := New(newFakeCache(), metrics,
		[]Provider{{Geocoder: hanging}, {Geocoder: healthy}},
		WithAttemptTimeout(50*time.Millisecond),
	)

	start := time.Now()
	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, healthy.reverseCalls)
	is.Less(time.Since(start), 2*time.Second, "the hanging attempt is capped, not waited out")
	is.Equal([]port.ProviderOutcome{port.ProviderError, port.ProviderSuccess}, outcomesOf(metrics),
		"a capped attempt is recorded as a provider error, so the error-rate alert sees it")
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

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	_, err := r.Reverse(ctx, query(cellNear))
	must.Error(err)
	must.ErrorIs(err, context.Canceled, "a hang-up is a cancellation, not a deadline")
	is.Equal(0, p2.reverseCalls, "a dead caller must not spend quota on the remaining providers")
	is.NotContains(outcomesOf(metrics), port.ProviderError,
		"a caller-side cancellation is not a provider failure")
}

// A deadline that ends an attempt (the request budget, or the per-attempt cap)
// must still be visible to the error-rate alert: it is an unanswered lookup, not
// a hang-up.
func TestReverseDeadlineCutRecordsProviderError(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	p1 := &fakeGeocoder{
		name: "p1",
		reverseFn: func(ctx context.Context, _ domain.ReverseQuery) ([]domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	r := newRouter(newFakeCache(), metrics, Provider{Geocoder: p1})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := r.Reverse(ctx, query(cellNear))
	must.ErrorIs(err, context.DeadlineExceeded)
	is.Equal([]port.ProviderOutcome{port.ProviderError}, outcomesOf(metrics))
}

func TestWithAttemptTimeoutRejectsNegative(t *testing.T) {
	must := require.New(t)
	must.Panics(func() {
		New(newFakeCache(), &fakeMetrics{}, nil, WithAttemptTimeout(-time.Second))
	})
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
				// Which provider serves depends on rotation; the total call
				// count is what this test is about.
				is.Equal(tt.wantCalls, p1.forwardCalls+p2.forwardCalls)
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

// healthy is a fake provider that answers every lookup with one result.
func healthy(name string) *fakeGeocoder {
	return reverseGeocoder(name, []domain.Result{house(cellAnswer.Lat(), cellAnswer.Lon())}, nil)
}

// Two instances of one provider type are separate identities: rotation gives
// each a share across successive lookups, and the provider metric label is the
// configured instance name, not the type.
func TestReverseRotatesAcrossInstances(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	// Both geocoders answer to the same type name; only the configured
	// instance name tells them apart.
	eu, us := healthy("chibigeo"), healthy("chibigeo")
	r := newRouter(newFakeCache(), metrics,
		Provider{Name: "chibi-eu", Geocoder: eu},
		Provider{Name: "chibi-us", Geocoder: us},
	)

	for _, cell := range []domain.Coordinates{cellNear, otherCell, cellThird} {
		got, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
		must.Len(got, 1)
	}
	is.Equal(2, eu.reverseCalls)
	is.Equal(1, us.reverseCalls)
	is.Equal([]string{"chibi-eu", "chibi-us", "chibi-eu"}, providerNames(metrics))
}

// Rotation is fair: four instances of one type each start a lookup in turn, so
// four providers paced at 1/s carry four lookups a second instead of three
// sitting idle behind the first.
func TestReverseRotationIsFair(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	names := []string{"a", "b", "c", "d"}
	geocoders := make([]*fakeGeocoder, 0, len(names))
	providers := make([]Provider, 0, len(names))
	for _, name := range names {
		g := healthy(name)
		geocoders = append(geocoders, g)
		providers = append(providers, Provider{Geocoder: g})
	}
	r := newRouter(newFakeCache(), metrics, providers...)

	for _, cell := range []domain.Coordinates{cellNear, otherCell, cellThird, cellFourth} {
		_, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
	}
	for i, g := range geocoders {
		is.Equal(1, g.reverseCalls, "provider %q", names[i])
	}
	is.Equal(names, providerNames(metrics), "successive lookups start at each instance in turn")
}

// With no configured instance name the provider's own name is the label, so a
// single entry keeps behaving as before.
func TestProviderNameFallsBackToGeocoderName(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	r := newRouter(newFakeCache(), metrics, Provider{Geocoder: healthy("chibigeo")})

	_, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	is.Equal([]string{"chibigeo"}, providerNames(metrics))
}

// The first tier carries the traffic: while any of its providers can answer,
// nothing in a later tier is called. The public rescue services must never take
// a steady share of an import.
func TestTierLaterTierUntouchedWhileFirstTierAvailable(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	t1a, t1b, rescue := healthy("t1-a"), healthy("t1-b"), healthy("rescue")
	// The rescue provider is listed first on purpose: grouping by tier, not by
	// config order, is what keeps it out of the rotation.
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 2, Geocoder: rescue},
		Provider{Tier: 1, Geocoder: t1a},
		Provider{Tier: 1, Geocoder: t1b},
	)

	for _, cell := range []domain.Coordinates{cellNear, otherCell, cellThird} {
		_, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
	}
	is.Equal(0, rescue.reverseCalls, "the rescue provider takes no share while tier 1 answers")
	is.Equal(2, t1a.reverseCalls)
	is.Equal(1, t1b.reverseCalls)
}

// A genuine "nothing here" is an answer, so the later tier stays untouched.
func TestTierLaterTierSkippedOnEmptyAnswer(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	empty := reverseGeocoder("empty", nil, nil)
	rescue := healthy("rescue")
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 2, Geocoder: rescue},
		Provider{Tier: 1, Geocoder: empty},
	)

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	is.Empty(got, "the caller gets the genuine empty answer")
	is.Equal(0, rescue.reverseCalls)
}

// An empty answer is final: within a shared tier it ends the walk just as it
// does across tiers, and the empty result is cached for the whole cell. This is
// the contract that makes an ocean point cheap, so the next instance in the
// tier is never tried.
func TestEmptyAnswerEndsTheWalkWithinATier(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	empty := reverseGeocoder("empty", nil, nil)
	next := healthy("next")
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 1, Geocoder: empty},
		Provider{Tier: 1, Geocoder: next},
	)

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	is.Empty(got, "the caller gets the genuine empty answer")
	is.Equal(0, next.reverseCalls, "an empty answer from the first instance ends the walk")

	// The empty answer is cached, so even a fresh lookup does not reach the
	// next instance.
	got, err = r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	is.Empty(got)
	is.Equal(0, next.reverseCalls)
	is.Equal([]port.CacheOutcome{port.CacheMiss, port.CacheHit}, metrics.cacheOutcomes)
}

// When every provider in the first tier fails, the lookup falls over to the
// rescue tier.
func TestTierLaterTierUsedWhenFirstTierFails(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	t1a := reverseGeocoder("t1-a", nil, errBoom)
	t1b := reverseGeocoder("t1-b", nil, errBoom)
	rescue := healthy("rescue")
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 2, Geocoder: rescue},
		Provider{Tier: 1, Geocoder: t1a},
		Provider{Tier: 1, Geocoder: t1b},
	)

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, rescue.reverseCalls)
	is.Equal([]port.ProviderOutcome{port.ProviderError, port.ProviderError, port.ProviderSuccess}, outcomesOf(metrics))
}

// When every provider in the first tier is out of quota, the lookup falls over
// to the rescue tier even though no attempt failed.
func TestTierLaterTierUsedWhenFirstTierOutOfQuota(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	t1a, t1b, rescue := healthy("t1-a"), healthy("t1-b"), healthy("rescue")
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 2, Geocoder: rescue},
		Provider{Tier: 1, Geocoder: t1a, DailyQuota: 1},
		Provider{Tier: 1, Geocoder: t1b, DailyQuota: 1},
	)

	for _, cell := range []domain.Coordinates{cellNear, otherCell, cellThird} {
		_, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
	}
	is.Equal(1, rescue.reverseCalls, "the third lookup finds tier 1 spent and falls over")
	is.Contains(outcomesOf(metrics), port.ProviderQuotaExhausted)
	is.Equal(1, t1a.reverseCalls)
	is.Equal(1, t1b.reverseCalls)
}

// Failover stays inside a tier before the next tier is reached.
func TestTierFailoverStaysWithinTier(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	t1a := reverseGeocoder("t1-a", nil, errBoom)
	t1b := healthy("t1-b")
	rescue := healthy("rescue")
	r := newRouter(newFakeCache(), metrics,
		Provider{Tier: 2, Geocoder: rescue},
		Provider{Tier: 1, Geocoder: t1a},
		Provider{Tier: 1, Geocoder: t1b},
	)

	got, err := r.Reverse(t.Context(), query(cellNear))
	must.NoError(err)
	must.Len(got, 1)
	is.Equal(1, t1b.reverseCalls)
	is.Equal(0, rescue.reverseCalls, "tier 1 still had a provider to try")
}

// Quota is per instance: each counter is independent and reports under the
// instance name.
func TestQuotaIsPerInstance(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	eu, us := healthy("chibigeo"), healthy("chibigeo")
	r := newRouter(newFakeCache(), metrics,
		Provider{Name: "chibi-eu", Geocoder: eu, DailyQuota: 1},
		Provider{Name: "chibi-us", Geocoder: us, DailyQuota: 1},
	)

	for _, cell := range []domain.Coordinates{cellNear, otherCell} {
		_, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
	}
	is.Equal(1, eu.reverseCalls)
	is.Equal(1, us.reverseCalls)
	is.Contains(metrics.quotas, quotaCall{name: "chibi-eu", remaining: 0})
	is.Contains(metrics.quotas, quotaCall{name: "chibi-us", remaining: 0})

	// Both instances are now spent: no provider is left to answer.
	_, err := r.Reverse(t.Context(), query(cellThird))
	must.ErrorIs(err, domain.ErrAllProvidersFailed)
}

// Pacing is per instance: each instance waits on its own limiter.
func TestPacingIsPerInstance(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	metrics := &fakeMetrics{}
	eu, us := healthy("chibigeo"), healthy("chibigeo")
	euLimiter, usLimiter := &countingLimiter{}, &countingLimiter{}
	r := newRouter(newFakeCache(), metrics,
		Provider{Name: "chibi-eu", Geocoder: eu, Limiter: euLimiter},
		Provider{Name: "chibi-us", Geocoder: us, Limiter: usLimiter},
	)

	for _, cell := range []domain.Coordinates{cellNear, otherCell} {
		_, err := r.Reverse(t.Context(), query(cell))
		must.NoError(err)
	}
	is.Equal(1, euLimiter.waits)
	is.Equal(1, usLimiter.waits)
}
