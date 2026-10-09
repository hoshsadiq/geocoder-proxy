package domain

// ReverseQuery asks for the address nearest to a point.
type ReverseQuery struct {
	Coordinates Coordinates
}

// ForwardQuery asks for places matching free text. Limit hints how many
// results the caller wants; providers cap it at their own maximum.
type ForwardQuery struct {
	Text  string
	Limit int
}
