// Package registry maps provider names to their client factories. Provider
// packages self-register in init() (the database/sql driver idiom, the one
// place init is acceptable in this repo); the composition root resolves the
// providers named in config and wires them into the router.
package registry

import (
	"fmt"
	"slices"
	"sync"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Config carries the per-provider settings resolved from user config.
// BaseURL defaults to the provider's public endpoint inside each factory;
// APIKey is empty for providers without auth; UserAgent identifies the
// application where a provider's policy requires it (Nominatim).
type Config struct {
	BaseURL   string
	APIKey    string
	UserAgent string
}

// Factory builds a provider client from its config.
type Factory func(cfg Config) (port.Geocoder, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a provider factory under its config name. Duplicate or nil
// registration is a programming error and panics, matching database/sql.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if f == nil {
		panic(fmt.Sprintf("registry: nil factory for %q", name))
	}
	if _, dup := factories[name]; dup {
		panic(fmt.Sprintf("registry: provider %q registered twice", name))
	}
	factories[name] = f
}

// Lookup returns the factory registered under name.
func Lookup(name string) (Factory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[name]
	return f, ok
}

// Names lists the registered provider names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
