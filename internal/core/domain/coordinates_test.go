package domain

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCoordinates(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		is, must := assert.New(t), require.New(t)
		for _, pair := range [][2]float64{{0, 0}, {90, 180}, {-90, -180}, {51.4773, -0.0614}} {
			c, err := NewCoordinates(pair[0], pair[1])
			must.NoError(err)
			is.InDelta(pair[0], c.Lat, 0)
			is.InDelta(pair[1], c.Lon, 0)
		}
	})

	t.Run("out of range", func(t *testing.T) {
		must := require.New(t)
		for _, pair := range [][2]float64{{90.0001, 0}, {-90.0001, 0}, {0, 180.0001}, {0, -180.0001}} {
			_, err := NewCoordinates(pair[0], pair[1])
			must.Error(err)
			must.ErrorIs(err, ErrInvalidCoordinates)
		}
	})

	t.Run("non-finite", func(t *testing.T) {
		must := require.New(t)
		// NaN passes every range comparison, so it needs its own guard:
		// strconv.ParseFloat("NaN", 64) succeeds, and a NaN cell key would be
		// a degenerate shared cell that poisons the cache.
		for _, pair := range [][2]float64{
			{math.NaN(), 0},
			{0, math.NaN()},
			{math.NaN(), math.NaN()},
			{math.Inf(1), 0},
			{0, math.Inf(-1)},
		} {
			_, err := NewCoordinates(pair[0], pair[1])
			must.Error(err)
			must.ErrorIs(err, ErrInvalidCoordinates)
		}
	})
}

func TestDistanceTo(t *testing.T) {
	is := assert.New(t)

	same := Coordinates{Lat: 51.4773, Lon: -0.0614}
	is.InDelta(0, same.DistanceTo(same), 0.0001)

	// 0.0001° of latitude is about 11.1 m everywhere on Earth.
	north := Coordinates{Lat: 51.4774, Lon: -0.0614}
	is.InDelta(11.12, same.DistanceTo(north), 0.5)

	// London (51.5074, -0.1278) to Paris (48.8566, 2.3522) is about 343 km.
	london := Coordinates{Lat: 51.5074, Lon: -0.1278}
	paris := Coordinates{Lat: 48.8566, Lon: 2.3522}
	is.InDelta(343000, london.DistanceTo(paris), 3000)

	// Near-antipodal pairs push the haversine's a past 1 through float noise;
	// the clamp keeps the result finite instead of NaN.
	antipodal := london.DistanceTo(Coordinates{Lat: -88.5, Lon: 1})
	is.False(math.IsNaN(antipodal))
	is.Greater(antipodal, 0.0)
}

func TestCellKey(t *testing.T) {
	is := assert.New(t)

	// Two points about 11 m apart share a cell.
	a := Coordinates{Lat: 51.47730, Lon: -0.06140}
	b := Coordinates{Lat: 51.47740, Lon: -0.06140}
	is.Equal(a.CellKey(), b.CellKey())

	// Points about 110 m apart do not.
	far := Coordinates{Lat: 51.47830, Lon: -0.06140}
	is.NotEqual(a.CellKey(), far.CellKey())

	// The key is stable against float representation noise.
	is.Equal(a.CellKey(), Coordinates{Lat: 51.477300000000004, Lon: -0.0614}.CellKey())
}
