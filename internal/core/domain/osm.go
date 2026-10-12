package domain

// CanonicalOsmType normalises a provider's OSM type value to the
// single-letter form (N, W, R) the domain and the Photon wire contract use.
// Providers send either the letters (Photon-shaped) or the words
// (Nominatim-shaped); anything else is unknown and becomes empty.
func CanonicalOsmType(raw string) string {
	switch raw {
	case "node", "N":
		return "N"
	case "way", "W":
		return "W"
	case "relation", "R":
		return "R"
	default:
		return ""
	}
}
