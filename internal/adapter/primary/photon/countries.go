package photon

// dawarichCountryNames maps ISO 3166-1 alpha-2 codes to the exact country
// names Dawarich resolves, for the codes where CLDR English disagrees.
// Dawarich matches countries by exact name against its Natural Earth seed
// (plus a small alias map), so a CLDR name like "Hong Kong SAR China" or
// "Myanmar (Burma)" never resolves. Derived from Dawarich 1.15.3
// lib/assets/countries.geojson.gz and its alias map; the table's test
// asserts it stays complete against that seed.
var dawarichCountryNames = map[string]string{
	"AG": "Antigua and Barbuda",
	"AX": "Aland",
	"BA": "Bosnia and Herzegovina",
	"BL": "Saint Barthelemy",
	"BS": "The Bahamas",
	"CD": "Democratic Republic of the Congo",
	"CG": "Republic of the Congo",
	"FM": "Federated States of Micronesia",
	"GS": "South Georgia and the Islands",
	"HK": "Hong Kong S.A.R.",
	"HM": "Heard Island and McDonald Islands",
	"KN": "Saint Kitts and Nevis",
	"LC": "Saint Lucia",
	"MF": "Saint Martin",
	"MK": "North Macedonia",
	"MM": "Myanmar",
	"MO": "Macao S.A.R",
	"PM": "Saint Pierre and Miquelon",
	"SH": "Saint Helena",
	"ST": "São Tomé and Principe",
	"SZ": "eSwatini",
	"TC": "Turks and Caicos Islands",
	"TF": "French Southern and Antarctic Lands",
	"TT": "Trinidad and Tobago",
	"UM": "United States Minor Outlying Islands",
	"VC": "Saint Vincent and the Grenadines",
	"VI": "United States Virgin Islands",
	"WF": "Wallis and Futuna",
}
