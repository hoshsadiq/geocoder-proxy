package registry

import (
	"context"
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

func TestRegisterAndLookup(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	factory := func(Config) (port.Geocoder, error) { return stubGeocoder{"stub"}, nil }

	Register("stub", factory)
	f, ok := Lookup("stub")
	must.True(ok)
	g, err := f(Config{})
	must.NoError(err)
	is.Equal("stub", g.Name())

	_, ok = Lookup("missing")
	is.False(ok)
	is.Contains(Names(), "stub")
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	require.Panics(t, func() {
		Register("dup", func(Config) (port.Geocoder, error) { return stubGeocoder{"a"}, nil })
		Register("dup", func(Config) (port.Geocoder, error) { return stubGeocoder{"b"}, nil })
	})
}

func TestRegisterPanicsOnNil(t *testing.T) {
	require.Panics(t, func() { Register("nilfactory", nil) })
}
