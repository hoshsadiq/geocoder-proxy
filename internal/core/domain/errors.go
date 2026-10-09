package domain

import "errors"

var (
	// ErrAllProvidersFailed means every configured provider errored on this
	// lookup. The caller must surface it as an error (the Photon adapter maps
	// it to 502), never as an empty result.
	ErrAllProvidersFailed = errors.New("all geocoding providers failed")

	// ErrNoProviders means the router was built without any provider; that is
	// a configuration error, not a lookup failure.
	ErrNoProviders = errors.New("no geocoding providers configured")
)
