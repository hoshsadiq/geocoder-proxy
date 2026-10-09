package tokenbucket

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPanicsOnNonPositiveRate(t *testing.T) {
	must := require.New(t)
	// rps <= 0 would block every Wait forever; that is a config error and
	// must fail at startup, not under load.
	must.Panics(func() { New(0) })
	must.Panics(func() { New(-1) })
}

func TestWait(t *testing.T) {
	t.Run("admits immediately at a high rate", func(t *testing.T) {
		must := require.New(t)
		must.NoError(New(1000).Wait(context.Background()))
	})

	t.Run("refuses a wait past the context deadline", func(t *testing.T) {
		is, must := assert.New(t), require.New(t)
		// One call per day: the second Wait can only end via the context, and
		// x/time/rate answers with its own "would exceed deadline" error (the
		// router treats that as pacing-limited and moves to the next provider).
		l := New(1.0 / 86400)
		must.NoError(l.Wait(context.Background()))

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := l.Wait(ctx)
		must.Error(err)
		is.ErrorContains(err, "would exceed")
	})

	t.Run("returns the context error when already cancelled", func(t *testing.T) {
		must := require.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		must.ErrorIs(New(1000).Wait(ctx), context.Canceled)
	})
}
