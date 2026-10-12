// Package jsonnum holds the tolerant numeric decoders the provider adapters
// share: upstreams disagree on whether numeric ids are quoted, bare, or
// missing, and a garbage value must degrade to zero rather than kill an
// otherwise usable answer.
package jsonnum

import (
	"encoding/json"
	"strconv"
)

// Int64 converts a JSON number (quoted or bare) to int64. Fractional and
// non-numeric values degrade to 0: a missing id loses nothing the caller
// can act on, while failing the whole response would.
func Int64(n json.Number) int64 {
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
		return int64(f)
	}
	return 0
}
