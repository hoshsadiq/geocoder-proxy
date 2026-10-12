// Package config loads the service configuration: an optional YAML file,
// overridden by GEOCODER_* environment variables (the loader also accepts
// explicit overrides, which tests use as the flag layer). It owns only the
// settings the service itself has: listeners, cache, and which providers to
// run. Each provider's settings stay an opaque map here; they are decoded
// and validated by the provider that owns them, through the registry.
package config

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Config is the validated runtime configuration.
type Config struct {
	// ListenAddr is the Photon API listener address.
	ListenAddr string
	// OpsAddr is the health/metrics listener address.
	OpsAddr string
	Cache   Cache
	// Providers are tried in order.
	Providers []ProviderEntry
}

// Cache configures the in-memory lookup cache.
type Cache struct {
	// TTL is how long an answer stays valid; zero means no expiry.
	TTL time.Duration
	// MaxEntries caps the cache; zero means the adapter default (100_000).
	MaxEntries int
	// MaxReuseDistance is how close a request must be to a house-numbered
	// answer's coordinates to reuse it, in metres.
	MaxReuseDistance float64
}

// ProviderEntry names a provider and carries its settings undecoded. The
// settings map is handed to the provider's own config type; its keys are
// the provider's business, not this package's.
type ProviderEntry struct {
	// Name is a registry key (chibigeo, geoapify, locationiq, nominatim).
	Name string
	// Settings are the provider's own keys, as read from the file and
	// overridden by GEOCODER_PROVIDER_<NAME>_<FIELD>.
	Settings map[string]any
}

// defaults returns the base configuration every layer overrides.
func defaults() Config {
	return Config{
		ListenAddr: ":8080",
		OpsAddr:    ":9090",
		Cache: Cache{
			TTL:              30 * 24 * time.Hour,
			MaxEntries:       100_000,
			MaxReuseDistance: 25,
		},
	}
}

// Validate checks the settings this package owns and returns every problem
// found, so the operator fixes all of them in one pass. Provider settings
// are checked by the provider that owns them.
func (c Config) Validate() error {
	var problems []string

	if c.ListenAddr == "" {
		problems = append(problems, "listen address is empty")
	}
	if c.OpsAddr == "" {
		problems = append(problems, "ops address is empty")
	}
	if c.ListenAddr != "" && c.ListenAddr == c.OpsAddr {
		problems = append(problems, "listen and ops addresses must differ")
	}
	if c.Cache.MaxReuseDistance <= 0 || math.IsNaN(c.Cache.MaxReuseDistance) || math.IsInf(c.Cache.MaxReuseDistance, 0) {
		problems = append(problems, "cache.max_reuse_distance must be a finite positive number")
	}
	if c.Cache.TTL < 0 {
		problems = append(problems, "cache.ttl must not be negative")
	}
	if c.Cache.MaxEntries < 0 {
		problems = append(problems, "cache.max_entries must not be negative")
	}
	if len(c.Providers) == 0 {
		problems = append(problems, "at least one provider is required")
	}
	seen := make(map[string]bool, len(c.Providers))
	for i, p := range c.Providers {
		if p.Name == "" {
			problems = append(problems, fmt.Sprintf("providers[%d]: name is empty", i))
			continue
		}
		if seen[p.Name] {
			problems = append(problems, fmt.Sprintf("provider %q is listed more than once", p.Name))
		}
		seen[p.Name] = true
	}

	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
