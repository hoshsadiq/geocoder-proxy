package domain

// Result is one geocoding answer. Coordinates is where the answer itself
// sits, which providers always return; the cache distance rule needs it.
//
// Lookups answer with a []Result: an empty slice with a nil error means the
// provider genuinely answered "nothing here", which is a cacheable answer.
// A failed lookup is always an error, never an empty slice, because
// downstream (Dawarich) treats an empty answer as final and never retries
// the point.
type Result struct {
	Address     Address
	Coordinates Coordinates
}
