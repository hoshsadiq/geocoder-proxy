package port

import (
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// Entry is one cached lookup. Result carries the reverse answer (or the
// genuine "nothing here", Found == false); Results carries forward answers.
// A cache entry holds one or the other, never both; the key prefix ("r:" or
// "f:") says which. StoredAt drives TTL expiry in cache implementations.
type Entry struct {
	Result   domain.Result
	Results  []domain.Result
	StoredAt time.Time
}

// Cache is the outgoing port for lookup storage. Keys are opaque to the
// implementation; the core derives them (cache cell for reverse, normalised
// text for forward). Implementations must be safe for concurrent use.
type Cache interface {
	Get(key string) (Entry, bool)
	Set(key string, entry Entry)
}
