package config

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entry(name string) ProviderEntry {
	return ProviderEntry{Name: name, Settings: map[string]any{"rate_per_second": 1}}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // empty means valid
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "no providers", mutate: func(c *Config) { c.Providers = nil }, wantErr: "at least one provider"},
		{name: "empty listen addr", mutate: func(c *Config) { c.ListenAddr = "" }, wantErr: "listen address"},
		{name: "empty ops addr", mutate: func(c *Config) { c.OpsAddr = "" }, wantErr: "ops address"},
		{name: "same listen and ops", mutate: func(c *Config) { c.OpsAddr = c.ListenAddr }, wantErr: "must differ"},
		{name: "zero reuse distance", mutate: func(c *Config) { c.Cache.MaxReuseDistance = 0 }, wantErr: "max_reuse_distance"},
		{name: "NaN reuse distance", mutate: func(c *Config) { c.Cache.MaxReuseDistance = math.NaN() }, wantErr: "max_reuse_distance"},
		{name: "infinite reuse distance", mutate: func(c *Config) { c.Cache.MaxReuseDistance = math.Inf(1) }, wantErr: "max_reuse_distance"},
		{name: "negative ttl", mutate: func(c *Config) { c.Cache.TTL = -time.Hour }, wantErr: "cache.ttl"},
		{name: "negative max entries", mutate: func(c *Config) { c.Cache.MaxEntries = -5 }, wantErr: "max_entries"},
		{name: "zero request timeout", mutate: func(c *Config) { c.RequestTimeout = 0 }, wantErr: "request_timeout"},
		{name: "negative request timeout", mutate: func(c *Config) { c.RequestTimeout = -time.Second }, wantErr: "request_timeout"},
		{name: "zero upstream timeout", mutate: func(c *Config) { c.UpstreamTimeout = 0 }, wantErr: "upstream_timeout"},
		{name: "upstream timeout above half the budget", mutate: func(c *Config) { c.UpstreamTimeout = c.RequestTimeout }, wantErr: "at most half"},
		{name: "upstream timeout at exactly half the budget", mutate: func(c *Config) { c.UpstreamTimeout = c.RequestTimeout / 2 }},
		{name: "duplicate providers", mutate: func(c *Config) { c.Providers = append(c.Providers, entry("chibigeo")) }, wantErr: "more than once"},
		{name: "empty provider name", mutate: func(c *Config) { c.Providers = []ProviderEntry{{Name: ""}} }, wantErr: "name is empty"},
		// Provider settings are deliberately not checked here: they belong
		// to the provider that owns them (see all_test's conformance suite).
		{name: "unknown provider name passes", mutate: func(c *Config) { c.Providers = []ProviderEntry{entry("future")} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cfg := defaults()
			cfg.Providers = []ProviderEntry{entry("chibigeo")}
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				must.NoError(err)
			} else {
				must.Error(err)
				is.ErrorContains(err, tt.wantErr)
			}
		})
	}
}

func TestValidateCollectsAllProblems(t *testing.T) {
	must := require.New(t)
	cfg := defaults()
	cfg.ListenAddr = ""
	cfg.Cache.MaxEntries = -1
	cfg.Providers = nil

	err := cfg.Validate()
	must.Error(err)
	// Every problem in one error, sorted.
	must.ErrorContains(err, "listen address")
	must.ErrorContains(err, "max_entries")
	must.ErrorContains(err, "at least one provider")
}

func TestDefaults(t *testing.T) {
	is := assert.New(t)
	d := defaults()
	is.Equal(":8080", d.ListenAddr)
	is.Equal(":9090", d.OpsAddr)
	is.Equal(30*24*time.Hour, d.Cache.TTL)
	is.Equal(100_000, d.Cache.MaxEntries)
	is.InDelta(25, d.Cache.MaxReuseDistance, 0.0001)
	is.Equal(DefaultRequestTimeout, d.RequestTimeout)
	is.Equal(DefaultUpstreamTimeout, d.UpstreamTimeout)
}

// The defaults must leave real failover room: one pace wait plus a capped
// attempt has to fit in the budget with a second provider still to try.
func TestDefaultTimeoutsLeaveFailoverRoom(t *testing.T) {
	is := assert.New(t)
	is.LessOrEqual(2*DefaultUpstreamTimeout, DefaultRequestTimeout,
		"the attempt cap must be at most half the budget")
}
