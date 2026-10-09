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
		Result:   domain.Result{Found: true, Address: domain.Address{Street: "Main Street"}},
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
	is.Equal("Main Street", got.Result.Address.Street)
}

func TestTTLExpiry(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	now := time.Now()
	clock := &fakeClock{t: now}
	c := New(time.Hour, WithClock(clock.Now))

	c.Set("k", port.Entry{Result: entry().Result, StoredAt: now})
	_, ok := c.Get("k")
	must.True(ok)

	clock.t = now.Add(2 * time.Hour)
	_, ok = c.Get("k")
	is.False(ok, "entry past its TTL must not be returned")

	// A zero TTL means no expiry.
	forever := New(0, WithClock(clock.Now))
	forever.Set("k", port.Entry{Result: entry().Result, StoredAt: now})
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
