package port

import (
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// Entry is one cached lookup: the list of results a lookup produced. An
// empty Results slice is a genuine "nothing here" answer. StoredAt drives
// TTL expiry in cache implementations.
type Entry struct {
	Results  []domain.Result
	StoredAt time.Time
}

// Cache is the outgoing port for lookup storage. Keys are opaque to the
// implementation; the core derives them (cache cell plus query options for
// reverse, normalised text plus limit for forward). Implementations must be
// safe for concurrent use and must not hand out slices the caller can mutate
// into the stored entry.
type Cache interface {
	Get(key string) (Entry, bool)
	Set(key string, entry Entry)
}
