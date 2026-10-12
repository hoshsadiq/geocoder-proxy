// Package registry maps provider names to their config factories and
// client builders. Provider packages self-register in init() (the
// database/sql driver idiom, the one place init is acceptable in this
// repo); the composition root resolves the providers named in config and
// wires them into the router. A provider is fully self-contained: it owns
// its configuration type, its validation, and its construction, and no
// other package names its fields.
package registry

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"sync"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Config is implemented by each provider's own configuration type. The
// composition root decodes operator configuration into it (mapstructure,
// with unknown keys rejected), validates it, and hands it to Build.
type Config interface {
	// Validate checks the provider's own requirements (credentials,
	// endpoints) and reports every problem.
	Validate() error
	// Rate is the configured requests-per-second pacing; must be a finite
	// positive number.
	Rate() float64
	// Quota is the configured daily call cap over a rolling 24h window;
	// zero means unlimited.
	Quota() int
}

// Registration is one provider's entry: how to make a fresh config and how
// to build the client from a validated one.
type Registration struct {
	New   func() Config
	Build func(Config) (port.Geocoder, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register adds a provider under its config name. Duplicate or invalid
// registration is a programming error and panics, matching database/sql.
func Register(name string, r Registration) {
	mu.Lock()
	defer mu.Unlock()
	if r.New == nil || r.Build == nil {
		panic(fmt.Sprintf("registry: incomplete registration for %q", name))
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("registry: provider %q registered twice", name))
	}
	registry[name] = r
}

// Lookup returns the registration for name.
func Lookup(name string) (Registration, bool) {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registry[name]
	return r, ok
}

// Names lists the registered provider names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ValidateLimits checks the rate and quota every provider's config carries.
// Shared so the rules live once while the fields stay provider-owned.
func ValidateLimits(rate float64, quota int) error {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return fmt.Errorf("rate_per_second must be a finite positive number")
	}
	if quota < 0 {
		return fmt.Errorf("daily_quota must not be negative")
	}
	return nil
}

// ValidateBaseURL checks a provider's endpoint override. Empty is valid
// (the provider's public default applies).
func ValidateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url is not a valid http(s) URL")
	}
	return nil
}
