package config

import (
	"fmt"
	"os"
	"strconv"
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

// scalarEnv maps the flat settings' environment variables to their koanf
// keys. Provider settings use GEOCODER_PROVIDERS and
// GEOCODER_PROVIDER_<NAME>_* instead; lists do not layer cleanly through a
// generic env transform, so they are merged explicitly.
var scalarEnv = map[string]string{
	"GEOCODER_LISTEN_ADDR":              "listen_addr",
	"GEOCODER_OPS_ADDR":                 "ops_addr",
	"GEOCODER_CACHE_TTL":                "cache.ttl",
	"GEOCODER_CACHE_MAX_ENTRIES":        "cache.max_entries",
	"GEOCODER_CACHE_MAX_REUSE_DISTANCE": "cache.max_reuse_distance",
}

// raw mirrors the YAML/env shape; strings become typed values in Load.
type raw struct {
	ListenAddr string     `koanf:"listen_addr"`
	OpsAddr    string     `koanf:"ops_addr"`
	Cache      rawCache   `koanf:"cache"`
	Providers  []Provider `koanf:"providers"`
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
		"listen_addr": d.ListenAddr,
		"ops_addr":    d.OpsAddr,
		"cache": map[string]any{
			"ttl":                d.Cache.TTL.String(),
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
			// they fail startup instead of being silently ignored.
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

	providers, err := mergeProviders(r.Providers)
	if err != nil {
		return Config{}, err
	}
	cfg.Providers = providers

	return cfg, cfg.Validate()
}

// mergeProviders applies the provider env layer: GEOCODER_PROVIDERS orders
// and selects providers by name, and GEOCODER_PROVIDER_<NAME>_* overrides
// each provider's settings. A provider named only in the env list starts
// from empty settings.
func mergeProviders(fromFile []Provider) ([]Provider, error) {
	byName := make(map[string]Provider, len(fromFile))
	var order []string
	for _, p := range fromFile {
		byName[p.Name] = p
		order = append(order, p.Name)
	}

	if list := os.Getenv(EnvPrefix + "PROVIDERS"); list != "" {
		order = order[:0]
		for name := range strings.SplitSeq(list, ",") {
			order = append(order, strings.TrimSpace(name))
		}
	}

	providers := make([]Provider, 0, len(order))
	for _, name := range order {
		p := byName[name]
		p.Name = name
		prefix := EnvPrefix + "PROVIDER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
		applyEnvString(&p.BaseURL, prefix+"BASE_URL")
		applyEnvString(&p.APIKey, prefix+"API_KEY")
		applyEnvString(&p.UserAgent, prefix+"USER_AGENT")
		if v, ok := os.LookupEnv(prefix + "RATE_PER_SECOND"); ok {
			rate, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", prefix+"RATE_PER_SECOND", v)
			}
			p.RatePerSecond = rate
		}
		if v, ok := os.LookupEnv(prefix + "DAILY_QUOTA"); ok {
			quota, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", prefix+"DAILY_QUOTA", v)
			}
			p.DailyQuota = quota
		}
		providers = append(providers, p)
	}
	return providers, nil
}

func applyEnvString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}
