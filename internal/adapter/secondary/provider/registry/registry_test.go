package registry

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/port"
)

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

func TestRegisterAndLookup(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	name := uniqueName("stub")
	factory := func(Config) (port.Geocoder, error) { return stubGeocoder{name}, nil }

	Register(name, factory)
	f, ok := Lookup(name)
	must.True(ok)
	g, err := f(Config{})
	must.NoError(err)
	is.Equal(name, g.Name())

	_, ok = Lookup("missing")
	is.False(ok)
	is.Contains(Names(), name)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	require.Panics(t, func() {
		name := uniqueName("dup")
		Register(name, func(Config) (port.Geocoder, error) { return stubGeocoder{"a"}, nil })
		Register(name, func(Config) (port.Geocoder, error) { return stubGeocoder{"b"}, nil })
	})
}

func TestRegisterPanicsOnNil(t *testing.T) {
	require.Panics(t, func() { Register(uniqueName("nilfactory"), nil) })
}
