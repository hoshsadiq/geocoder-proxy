// Package registry maps provider names to their config factories and
// client builders. Provider packages self-register in init() (the
// database/sql driver idiom, the one place init is acceptable in this
// repo); the composition root resolves the providers named in config and
// wires them into the router. A provider is fully self-contained: it owns
// its configuration type, its validation, and its construction, and no
// other package names its fields.
package registry

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"sync"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

// Config is implemented by each provider's own configuration type. The
// composition root decodes operator configuration into it (koanf tags,
// with unknown keys rejected), validates it, and hands it to Build.
// A Config carrying credentials must never be logged or serialised.
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
// to build the client from a validated one. Build re-validates through the
// provider's constructor, so the client is safe however it is reached.
type Registration struct {
	NewConfig func() Config
	Build     func(Config) (port.Geocoder, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register adds a provider under its config name. Called from provider
// packages' init() only. Duplicate or invalid registration is a
// programming error and panics, matching database/sql.
func Register(name string, r Registration) {
	mu.Lock()
	defer mu.Unlock()
	if r.NewConfig == nil || r.Build == nil {
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
		return errors.New("rate_per_second must be a finite positive number")
	}
	if quota < 0 {
		return errors.New("daily_quota must not be negative")
	}
	return nil
}

// ValidateBaseURL checks a provider's endpoint override. Empty is valid
// (the provider's public default applies). Credentials, query and fragment
// are rejected: credentials must not ride the URL, and the adapters
// concatenate path and query onto the base, so a pre-filled one breaks the
// request.
func ValidateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("base_url is not a valid http(s) URL")
	}
	if u.User != nil {
		return errors.New("base_url must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base_url must not carry a query or fragment")
	}
	return nil
}

// Decode unmarshals operator settings into cfg: unknown keys are rejected
// (a typo must fail startup, not be ignored), string values are coerced
// (the environment layer produces strings), and a non-integral number is
// refused rather than truncated — a fractional daily_quota would otherwise
// silently become a different budget than the operator wrote.
func Decode(cfg Config, settings map[string]any) error {
	k := koanf.New(".")
	// A static confmap cannot fail to load.
	_ = k.Load(confmap.Provider(settings, "."), nil)
	return k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			ErrorUnused:      true,
			Result:           cfg,
			WeaklyTypedInput: true,
			DecodeHook:       integralNumber(),
		},
	})
}

// integralNumber coerces an integral number (float or string) into the int
// field it targets, and refuses a fractional one.
func integralNumber() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Value) (any, error) {
		if t.Kind() != reflect.Int && t.Kind() != reflect.Int64 {
			return f.Interface(), nil
		}
		var value float64
		switch f.Kind() {
		case reflect.Float64:
			value = f.Float()
		case reflect.String:
			// A non-numeric string is not this hook's business: the
			// standard coercion decides (and usually rejects) it.
			if v, err := strconv.ParseFloat(f.String(), 64); err == nil {
				value = v
			} else {
				return f.Interface(), nil
			}
		default:
			return f.Interface(), nil
		}
		if value != math.Trunc(value) {
			return nil, fmt.Errorf("value %v is not a whole number", value)
		}
		return int64(value), nil
	}
}
