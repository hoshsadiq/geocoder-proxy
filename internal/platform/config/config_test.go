package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsValidate(t *testing.T) {
	must := require.New(t)
	must.Error(defaults().Validate(), "no providers configured is invalid")
}

func TestValidate(t *testing.T) {
	validProvider := Provider{Name: "chibigeo", RatePerSecond: 1}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // empty means valid
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "no providers", mutate: func(c *Config) { c.Providers = nil }, wantErr: "at least one provider"},
		{name: "empty listen addr", mutate: func(c *Config) { c.ListenAddr = "" }, wantErr: "listen address"},
		{name: "zero reuse distance", mutate: func(c *Config) { c.Cache.MaxReuseDistance = 0 }, wantErr: "max_reuse_distance"},
		{name: "zero rate", mutate: func(c *Config) { c.Providers[0].RatePerSecond = 0 }, wantErr: "rate_per_second must be positive"},
		{name: "negative quota", mutate: func(c *Config) { c.Providers[0].DailyQuota = -1 }, wantErr: "daily_quota"},
		{name: "nominatim without user agent", mutate: func(c *Config) { c.Providers[0].Name = "nominatim" }, wantErr: "user_agent"},
		{name: "geoapify without key", mutate: func(c *Config) { c.Providers[0].Name = "geoapify" }, wantErr: "api_key"},
		{name: "unknown provider name passes", mutate: func(c *Config) { c.Providers[0].Name = "future" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			cfg := defaults()
			cfg.Providers = []Provider{validProvider}
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
	err := defaults().Validate()
	must.Error(err)
	must.ErrorContains(err, "provider")
}

func TestDefaults(t *testing.T) {
	is := assert.New(t)
	d := defaults()
	is.Equal(":8080", d.ListenAddr)
	is.Equal(":9090", d.OpsAddr)
	is.Equal(30*24*time.Hour, d.Cache.TTL)
	is.InDelta(25, d.Cache.MaxReuseDistance, 0.0001)
}
