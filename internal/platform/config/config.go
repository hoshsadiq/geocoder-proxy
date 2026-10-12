// Package config loads and validates the service configuration: an
// optional YAML file, overridden by GEOCODER_* environment variables (the
// loader also accepts explicit overrides, which tests use as the flag
// layer). The typed Config is the only thing the rest of the binary sees;
// all user-facing validation errors are collected and reported here, so a
// misconfigured service fails at startup with every problem listed, not at
// the first request.
package config

import (
	"fmt"
	"math"
	"net/url"
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
	Providers []Provider
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

// Provider is one upstream geocoder's configuration.
type Provider struct {
	// Name matches a registry key (chibigeo, geoapify, locationiq, nominatim).
	Name string `koanf:"name"`
	// BaseURL overrides the provider's public endpoint (e.g. self-hosted).
	BaseURL string `koanf:"base_url"`
	// APIKey is the provider credential; whether it is required depends on
	// the provider.
	APIKey string `koanf:"api_key"`
	// UserAgent identifies the application where the provider's policy
	// requires it (Nominatim).
	UserAgent string `koanf:"user_agent"`
	// RatePerSecond paces calls; required, must be positive.
	RatePerSecond float64 `koanf:"rate_per_second"`
	// DailyQuota caps calls over a rolling 24-hour window; zero means
	// unlimited.
	DailyQuota int `koanf:"daily_quota"`
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

// Validate checks the assembled configuration and returns every problem
// found, so the operator fixes all of them in one pass.
func (c Config) Validate() error {
	var problems []string

	if c.ListenAddr == "" {
		problems = append(problems, "listen address is empty")
	}
	if c.OpsAddr == "" {
		problems = append(problems, "ops address is empty")
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
	if c.ListenAddr != "" && c.ListenAddr == c.OpsAddr {
		problems = append(problems, "listen and ops addresses must differ")
	}
	if len(c.Providers) == 0 {
		problems = append(problems, "at least one provider is required")
	}
	for i, p := range c.Providers {
		prefix := fmt.Sprintf("providers[%d] (%s)", i, p.Name)
		if p.Name == "" {
			problems = append(problems, fmt.Sprintf("providers[%d]: name is empty", i))
			continue
		}
		if p.RatePerSecond <= 0 || math.IsNaN(p.RatePerSecond) || math.IsInf(p.RatePerSecond, 0) {
			problems = append(problems, prefix+": rate_per_second must be a finite positive number")
		}
		if p.DailyQuota < 0 {
			problems = append(problems, prefix+": daily_quota must not be negative")
		}
		if p.BaseURL != "" {
			if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				problems = append(problems, prefix+": base_url is not a valid http(s) URL")
			}
		}
		switch p.Name {
		case "nominatim":
			if p.UserAgent == "" {
				problems = append(problems, prefix+": user_agent is required by the Nominatim usage policy")
			}
		case "geoapify", "locationiq":
			if p.APIKey == "" {
				problems = append(problems, prefix+": api_key is required")
			}
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	seen := make(map[string]bool, len(c.Providers))
	for _, p := range c.Providers {
		if seen[p.Name] {
			return fmt.Errorf("invalid configuration:\n  - provider %q is listed more than once", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}
