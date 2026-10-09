package domain

// Address is a normalised postal address. Every provider adapter maps its own
// response shape onto this, so the core never sees provider-specific fields.
// Any field may be empty; providers vary widely in what they return.
//
// The OSM identity fields exist because downstream consumers do more than
// display the address: Dawarich uses Name for venue naming and the OSM
// identifiers for place search and place creation, so dropping them is a
// silent feature regression.
type Address struct {
	// Name is the place's own name (venue, POI), when the answer is one.
	Name string
	// Type is the provider's category for the answer (Photon's `type`:
	// house, street, locality...). Free-form because providers differ.
	Type string

	HouseNumber string
	Street      string
	Postcode    string
	City        string
	Locality    string
	District    string
	County      string
	State       string
	Country     string
	CountryCode string

	// OSM identifiers of the matched feature. OsmType is the provider's raw
	// value (Photon uses N/W/R); adapters must not translate it.
	OsmID    int64
	OsmType  string
	OsmKey   string
	OsmValue string
}

// HasHouseNumber reports whether the answer is precise to a building. The
// cache reuse rule treats house-numbered answers strictly (distance check)
// and street-level answers loosely, because a street name does not change
// inside one cache cell.
func (a Address) HasHouseNumber() bool {
	return a.HouseNumber != ""
}
