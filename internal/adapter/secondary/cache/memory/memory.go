// Package memory is the in-memory Cache adapter: a map guarded by a RWMutex,
// with TTL expiry and a size cap. The service is deliberately stateless
// across restarts: losing the cache costs re-queries against free quotas,
// never data.
package memory

import (
	"slices"
	"sync"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Compile-time guard.
var _ port.Cache = (*Cache)(nil)

// Cache is an in-memory port.Cache. Safe for concurrent use.
type Cache struct {
	mu         sync.RWMutex
	entries    map[string]port.Entry
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

// Option customises a Cache.
type Option func(*Cache)

// WithMaxEntries caps the entry count; the oldest entries are dropped when
// the cap is exceeded. Zero means unbounded, which is fine for a location
// history but dangerous if query patterns change.
func WithMaxEntries(n int) Option {
	return func(c *Cache) { c.maxEntries = n }
}

// WithClock overrides the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(c *Cache) { c.now = now }
}

// New returns a Cache whose entries expire after ttl. A zero ttl means
// entries never expire; addresses do not move, so a generous ttl (days to
// weeks) mainly bounds staleness after the map survives a street rename.
func New(ttl time.Duration, opts ...Option) *Cache {
	c := &Cache{
		entries: make(map[string]port.Entry),
		ttl:     ttl,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the entry for key unless it is missing or expired.
func (c *Cache) Get(key string) (port.Entry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return port.Entry{}, false
	}
	if c.ttl > 0 && c.now().Sub(entry.StoredAt) > c.ttl {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return port.Entry{}, false
	}
	return entry, true
}

// Set stores the entry, evicting the oldest entries first when the cache is
// over its cap.
func (c *Cache) Set(key string, entry port.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = entry
	if c.maxEntries <= 0 || len(c.entries) <= c.maxEntries {
		return
	}

	// Drop the oldest quarter in one sweep: an exact LRU is not worth the
	// bookkeeping for a lookup cache.
	excess := len(c.entries) - c.maxEntries + c.maxEntries/4
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return c.entries[a].StoredAt.Compare(c.entries[b].StoredAt)
	})
	for _, k := range keys[:excess] {
		delete(c.entries, k)
	}
}
