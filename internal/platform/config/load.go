package config

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix prefixes every environment variable the loader reads.
const EnvPrefix = "GEOCODER_"

// providerEnvPrefix precedes a provider's own settings:
// GEOCODER_PROVIDER_<NAME>_<FIELD> sets FIELD on provider NAME.
const providerEnvPrefix = EnvPrefix + "PROVIDER_"

// scalarEnv maps the service's own environment variables to their koanf
// keys. Provider settings use GEOCODER_PROVIDERS and
// GEOCODER_PROVIDER_<NAME>_* instead; those are merged by name below,
// because a list does not layer cleanly through a generic env transform.
var scalarEnv = map[string]string{
	"GEOCODER_LISTEN_ADDR":              "listen_addr",
	"GEOCODER_OPS_ADDR":                 "ops_addr",
	"GEOCODER_REQUEST_TIMEOUT":          "request_timeout",
	"GEOCODER_UPSTREAM_TIMEOUT":         "upstream_timeout",
	"GEOCODER_CACHE_TTL":                "cache.ttl",
	"GEOCODER_CACHE_MAX_ENTRIES":        "cache.max_entries",
	"GEOCODER_CACHE_MAX_REUSE_DISTANCE": "cache.max_reuse_distance",
}

// raw mirrors the file and env shape. Providers stay undecoded: each entry
// is a name plus that provider's own keys.
type raw struct {
	ListenAddr      string           `koanf:"listen_addr"`
	OpsAddr         string           `koanf:"ops_addr"`
	RequestTimeout  string           `koanf:"request_timeout"`
	UpstreamTimeout string           `koanf:"upstream_timeout"`
	Cache           rawCache         `koanf:"cache"`
	Providers       []map[string]any `koanf:"providers"`
}

type rawCache struct {
	TTL              string  `koanf:"ttl"`
	MaxEntries       int     `koanf:"max_entries"`
	MaxReuseDistance float64 `koanf:"max_reuse_distance"`
}

// Load assembles the configuration: defaults, then the YAML file when path
// is non-empty, then GEOCODER_* environment variables, then flag overrides.
// An empty path with a GEOCODER_CONFIG variable set uses that file instead.
func Load(path string, flagOverrides map[string]any) (Config, error) {
	k := koanf.New(".")

	d := defaults()
	// Loading a static confmap cannot fail; the file load below is the one
	// that surfaces errors.
	_ = k.Load(confmap.Provider(map[string]any{
		"listen_addr":      d.ListenAddr,
		"ops_addr":         d.OpsAddr,
		"request_timeout":  d.RequestTimeout.String(),
		"upstream_timeout": d.UpstreamTimeout.String(),
		"cache": map[string]any{
			"ttl":                d.Cache.TTL.String(),
			"max_entries":        d.Cache.MaxEntries,
			"max_reuse_distance": d.Cache.MaxReuseDistance,
		},
	}, "."), nil)

	if path == "" {
		path = os.Getenv(EnvPrefix + "CONFIG")
	}
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("loading config file: %w", err)
		}
	}

	_ = k.Load(env.Provider(EnvPrefix, ".", func(s string) string {
		return scalarEnv[s]
	}), nil)

	if len(flagOverrides) > 0 {
		_ = k.Load(confmap.Provider(flagOverrides, "."), nil)
	}

	var r raw
	if err := k.UnmarshalWithConf("", &r, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			// Unknown keys are typos the operator meant to have an effect;
			// they fail startup instead of being silently ignored. Provider
			// settings are checked by the provider's own strict decode.
			ErrorUnused:      true,
			Result:           &r,
			WeaklyTypedInput: true,
		},
	}); err != nil {
		return Config{}, fmt.Errorf("parsing configuration: %w", err)
	}

	cfg := Config{
		ListenAddr: r.ListenAddr,
		OpsAddr:    r.OpsAddr,
		Cache: Cache{
			MaxEntries:       r.Cache.MaxEntries,
			MaxReuseDistance: r.Cache.MaxReuseDistance,
		},
	}
	if r.Cache.TTL != "" {
		ttl, err := time.ParseDuration(r.Cache.TTL)
		if err != nil {
			return Config{}, fmt.Errorf("cache.ttl: %w", err)
		}
		cfg.Cache.TTL = ttl
	}
	if r.RequestTimeout != "" {
		requestTimeout, err := time.ParseDuration(r.RequestTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("request_timeout: %w", err)
		}
		cfg.RequestTimeout = requestTimeout
	}
	if r.UpstreamTimeout != "" {
		upstreamTimeout, err := time.ParseDuration(r.UpstreamTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("upstream_timeout: %w", err)
		}
		cfg.UpstreamTimeout = upstreamTimeout
	}

	cfg.Providers = providerEntries(r.Providers)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := unknownProviderEnv(cfg.Providers); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// unknownProviderEnv catches a GEOCODER_PROVIDER_<NAME>_* variable whose
// NAME matches no configured provider: a typo'd name would otherwise be
// silently ignored while the operator believes the override took effect.
// A name is recognised when some configured provider's prefix is a prefix
// of the variable, which needs no assumption about underscores in names.
func unknownProviderEnv(entries []ProviderEntry) error {
	prefixes := make([]string, 0, len(entries))
	for _, e := range entries {
		prefixes = append(prefixes, providerEnvPrefix+strings.ToUpper(strings.ReplaceAll(e.Name, "-", "_"))+"_")
	}
	var problems []string
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, providerEnvPrefix) {
			continue
		}
		known := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				known = true
				break
			}
		}
		if !known {
			problems = append(problems, fmt.Sprintf("%s names no configured provider", key))
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// providerEntries turns the file's provider list into entries: it lifts the
// name out, applies the per-provider environment overrides, and orders the
// result the way GEOCODER_PROVIDERS names it when that variable is set.
func providerEntries(fromFile []map[string]any) []ProviderEntry {
	type source struct {
		settings map[string]any
	}
	byName := make(map[string]source, len(fromFile))
	var order []string
	for _, entry := range fromFile {
		name, _ := entry["name"].(string)
		settings := make(map[string]any, len(entry))
		for k, v := range entry {
			if k == "name" {
				continue
			}
			settings[k] = v
		}
		byName[name] = source{settings: settings}
		order = append(order, name)
	}

	if list := os.Getenv(EnvPrefix + "PROVIDERS"); list != "" {
		order = order[:0]
		for name := range strings.SplitSeq(list, ",") {
			order = append(order, strings.TrimSpace(name))
		}
	}

	entries := make([]ProviderEntry, 0, len(order))
	for _, name := range order {
		s := byName[name]
		settings := make(map[string]any, len(s.settings))
		maps.Copy(settings, s.settings)
		applyProviderEnv(settings, name)
		entries = append(entries, ProviderEntry{Name: name, Settings: settings})
	}
	return entries
}

// applyProviderEnv overlays GEOCODER_PROVIDER_<NAME>_<FIELD> onto a
// provider's settings. The field suffix is lowercased and otherwise
// untouched, so a provider's own new key needs no change here. Values are
// strings; the provider's decode coerces them.
func applyProviderEnv(settings map[string]any, name string) {
	prefix := providerEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		settings[strings.ToLower(strings.TrimPrefix(key, prefix))] = value
	}
}
