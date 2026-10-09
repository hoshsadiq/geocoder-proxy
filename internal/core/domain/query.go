package domain

// ReverseQuery asks for the places nearest to a point. Limit, Radius and
// DistanceSort are passed through to the provider: Dawarich sends
// limit=10, radius=1, distance_sort=true on its places flow, and silently
// dropping them collapses that flow to one feature. A Limit of zero means
// the provider's default.
type ReverseQuery struct {
	Coordinates  Coordinates
	Limit        int
	Radius       float64
	DistanceSort bool
}

// ForwardQuery asks for places matching free text. Limit hints how many
// results the caller wants; providers cap it at their own maximum.
type ForwardQuery struct {
	Text  string
	Limit int
}
