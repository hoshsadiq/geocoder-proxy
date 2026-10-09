package domain

// Address is a normalised postal address. Every provider adapter maps its own
// response shape onto this, so the core never sees provider-specific fields.
// Any field may be empty; providers vary widely in what they return.
type Address struct {
	HouseNumber string
	Street      string
	Postcode    string
	City        string
	State       string
	Country     string
	CountryCode string
}

// HasHouseNumber reports whether the answer is precise to a building. The
// cache reuse rule treats house-numbered answers strictly (distance check)
// and street-level answers loosely, because a street name does not change
// inside one cache cell.
func (a Address) HasHouseNumber() bool {
	return a.HouseNumber != ""
}
