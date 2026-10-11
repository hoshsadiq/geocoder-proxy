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
// the cap is exceeded. The default is 100_000; pass 0 explicitly for an
// unbounded cache (dangerous: key count is caller-driven).
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
		entries:    make(map[string]port.Entry),
		ttl:        ttl,
		maxEntries: 100_000,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the entry for key unless it is missing or expired. The
// returned slice is a copy; mutating it cannot corrupt the stored entry.
func (c *Cache) Get(key string) (port.Entry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return port.Entry{}, false
	}
	if c.ttl > 0 && c.now().Sub(entry.StoredAt) > c.ttl {
		// Re-check under the write lock: a concurrent Set may have replaced
		// the stale entry with a fresh one, which must not be deleted.
		c.mu.Lock()
		if cur, ok := c.entries[key]; ok && c.now().Sub(cur.StoredAt) > c.ttl {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return port.Entry{}, false
	}
	entry.Results = slices.Clone(entry.Results)
	return entry, true
}

// Set stores a copy of the entry, evicting the oldest entries first when the
// cache is over its cap.
func (c *Cache) Set(key string, entry port.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry.Results = slices.Clone(entry.Results)
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
