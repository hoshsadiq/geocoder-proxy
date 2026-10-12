package registry

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

type stubConfig struct {
	rate  float64
	quota int
}

func (c *stubConfig) Validate() error { return ValidateLimits(c.rate, c.quota) }
func (c *stubConfig) Rate() float64   { return c.rate }
func (c *stubConfig) Quota() int      { return c.quota }

type stubGeocoder struct{ name string }

func (s stubGeocoder) Name() string { return s.name }

func (s stubGeocoder) Reverse(context.Context, domain.ReverseQuery) ([]domain.Result, error) {
	return nil, nil
}

func (s stubGeocoder) Forward(context.Context, domain.ForwardQuery) ([]domain.Result, error) {
	return nil, nil
}

var _ port.Geocoder = stubGeocoder{}

var testNameCounter atomic.Int64

// uniqueName gives each test its own registration slot: the registry is a
// process-global map, so reusing a fixed name breaks `-count=2` runs.
func uniqueName(base string) string {
	return fmt.Sprintf("%s-%d", base, testNameCounter.Add(1))
}

func stubRegistration(name string) Registration {
	return Registration{
		NewConfig: func() Config { return &stubConfig{rate: 1} },
		Build:     func(Config) (port.Geocoder, error) { return stubGeocoder{name}, nil },
	}
}

func TestRegisterAndLookup(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	name := uniqueName("stub")
	Register(name, stubRegistration(name))

	r, ok := Lookup(name)
	must.True(ok)
	g, err := r.Build(r.NewConfig())
	must.NoError(err)
	is.Equal(name, g.Name(), "Lookup must return the registration made under this name")

	_, ok = Lookup("missing")
	is.False(ok)
	is.Contains(Names(), name)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	require.Panics(t, func() {
		name := uniqueName("dup")
		Register(name, stubRegistration(name))
		Register(name, stubRegistration(name))
	})
}

func TestRegisterPanicsOnIncomplete(t *testing.T) {
	require.Panics(t, func() { Register(uniqueName("nilnew"), Registration{}) })
	require.Panics(t, func() {
		Register(uniqueName("nilbuild"), Registration{NewConfig: func() Config { return &stubConfig{rate: 1} }})
	})
}

func TestValidateLimits(t *testing.T) {
	must := require.New(t)
	must.NoError(ValidateLimits(1, 0))
	must.NoError(ValidateLimits(0.5, 100))
	must.Error(ValidateLimits(0, 0))
	must.Error(ValidateLimits(-1, 0))
	must.Error(ValidateLimits(1, -1))
	must.Error(ValidateLimits(2.5, -2))
	must.Error(ValidateLimits(math.NaN(), 0))
	must.Error(ValidateLimits(math.Inf(1), 0))
}

func TestValidateBaseURL(t *testing.T) {
	must := require.New(t)
	must.NoError(ValidateBaseURL(""))
	must.NoError(ValidateBaseURL("https://app.chibigeo.com/v1/photon"))
	must.NoError(ValidateBaseURL("http://localhost:2322"))
	must.Error(ValidateBaseURL("not a url"))
	must.Error(ValidateBaseURL("ftp://example.com"))
	must.Error(ValidateBaseURL("http://"))
	must.Error(ValidateBaseURL("https://user:pass@example.com"), "credentials must not ride the URL")
	must.Error(ValidateBaseURL("http://example.com?x=1"))
	must.Error(ValidateBaseURL("http://example.com#frag"))
}
