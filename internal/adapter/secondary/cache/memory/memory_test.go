package memory

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

func entry() port.Entry {
	return port.Entry{
		Results:  []domain.Result{{Address: domain.Address{Street: "Main Street"}}},
		StoredAt: time.Now(),
	}
}

func TestGetSet(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	c := New(0)

	_, ok := c.Get("missing")
	is.False(ok)

	c.Set("k", entry())
	got, ok := c.Get("k")
	must.True(ok)
	is.Equal("Main Street", got.Results[0].Address.Street)
}

func TestEntriesAreCopied(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	c := New(0)

	in := entry()
	c.Set("k", in)
	// Mutating the caller's slice after Set must not reach the cache.
	in.Results[0].Address.Street = "Forged Street"
	got, ok := c.Get("k")
	must.True(ok)
	is.Equal("Main Street", got.Results[0].Address.Street)

	// Mutating a Get result must not reach the next reader.
	got.Results[0].Address.Street = "Forged Street"
	again, ok := c.Get("k")
	must.True(ok)
	is.Equal("Main Street", again.Results[0].Address.Street)
}

func TestTTLExpiry(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	now := time.Now()
	clock := &fakeClock{t: now}
	c := New(time.Hour, WithClock(clock.Now))

	c.Set("k", port.Entry{Results: entry().Results, StoredAt: now})
	_, ok := c.Get("k")
	must.True(ok)

	clock.t = now.Add(2 * time.Hour)
	_, ok = c.Get("k")
	is.False(ok, "entry past its TTL must not be returned")

	// A zero TTL means no expiry.
	forever := New(0, WithClock(clock.Now))
	forever.Set("k", port.Entry{Results: entry().Results, StoredAt: now})
	_, ok = forever.Get("k")
	is.True(ok)
}

func TestMaxEntriesEvictsOldest(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	base := time.Now()
	c := New(0, WithMaxEntries(10))

	for i := range 10 {
		c.Set(string(rune('a'+i)), port.Entry{StoredAt: base.Add(time.Duration(i) * time.Second)})
	}
	// The oldest ("a") is dropped when the cap is exceeded.
	c.Set("z", port.Entry{StoredAt: base.Add(time.Minute)})

	_, ok := c.Get("a")
	is.False(ok, "oldest entry should have been evicted")
	_, ok = c.Get("z")
	must.True(ok)
	must.LessOrEqual(len(c.entries), 10)
}

func TestDefaultCapIsBounded(t *testing.T) {
	must := require.New(t)
	must.Equal(100_000, New(0).maxEntries, "the default cache must not be unbounded")
	must.Equal(0, New(0, WithMaxEntries(0)).maxEntries, "explicit zero stays unbounded")
}

func TestExpiredGetDoesNotDeleteFreshEntry(t *testing.T) {
	// The QA probe that found the read-check-delete race: a Get that read a
	// stale entry must not delete a fresh entry Set in the meantime. The
	// fixed Get re-checks expiry under the write lock, so hammering the two
	// interleaved never loses a fresh write.
	must := require.New(t)
	now := time.Now()
	clock := &fakeClock{t: now}
	c := New(time.Hour, WithClock(clock.Now))

	for range 200 {
		c.Set("k", port.Entry{StoredAt: now.Add(-2 * time.Hour)}) // stale
		clock.t = now
		var wg sync.WaitGroup
		wg.Go(func() { c.Get("k") })                            // reads stale, wants to delete
		wg.Go(func() { c.Set("k", port.Entry{StoredAt: now}) }) // fresh write
		wg.Wait()
		_, ok := c.Get("k")
		must.True(ok, "a fresh entry must survive a concurrent stale read")
		clock.t = now.Add(2 * time.Hour)
		c.mu.Lock()
		delete(c.entries, "k")
		c.mu.Unlock()
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(time.Minute)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			key := string(rune('a' + i%5))
			c.Set(key, entry())
			c.Get(key)
		})
	}
	wg.Wait()
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
