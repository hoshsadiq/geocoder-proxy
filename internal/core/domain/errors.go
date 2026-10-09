package domain

import "errors"

var (
	// ErrAllProvidersFailed means every configured provider errored on this
	// lookup. The caller must surface it as an error (the Photon adapter maps
	// it to 503, not 502: the geocoder gem Dawarich uses raises only on
	// 400/401/402/429/503 and would parse a JSON-bodied 502 into the empty
	// result the never-blank rule exists to prevent), never as an empty
	// result.
	ErrAllProvidersFailed = errors.New("all geocoding providers failed")

	// ErrNoProviders means the router was built without any provider; that is
	// a configuration error, not a lookup failure.
	ErrNoProviders = errors.New("no geocoding providers configured")

	// ErrInvalidCoordinates rejects out-of-range or non-finite coordinate
	// input. Adapters map it to a client error (400).
	ErrInvalidCoordinates = errors.New("invalid coordinates")

	// ErrInvalidQuery rejects malformed query input (e.g. forward text over
	// the length cap). Adapters map it to a client error (400).
	ErrInvalidQuery = errors.New("invalid query")
)
