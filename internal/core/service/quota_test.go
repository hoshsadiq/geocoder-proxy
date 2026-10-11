package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestQuotaCounter(t *testing.T) {
	is := assert.New(t)
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	q := newQuotaCounter(2)

	is.True(q.admit(start))
	is.Equal(1, q.remaining(start))
	is.True(q.admit(start.Add(time.Minute)))
	is.False(q.admit(start.Add(2*time.Minute)), "the third call inside the window is over quota")
	is.Equal(0, q.remaining(start.Add(2*time.Minute)))

	// The oldest slot returns exactly 24h after its call.
	is.False(q.admit(start.Add(23*time.Hour)), "still over quota just before the oldest call ages out")
	is.True(q.admit(start.Add(24*time.Hour+time.Second)), "the oldest call has left the window")
	is.Equal(0, q.remaining(start.Add(24*time.Hour+time.Second)))
}

func TestQuotaCounterCapsEveryProviderDay(t *testing.T) {
	is := assert.New(t)
	// Whatever the provider's reset time is, a rolling 24h cap of N admits at
	// most N calls inside any 24h span, including any calendar day.
	const limit = 5
	q := newQuotaCounter(limit)
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)

	admitted := 0
	for i := range 2000 {
		if q.admit(start.Add(time.Duration(i) * 30 * time.Second)) {
			admitted++
		}
	}
	is.Equal(limit, admitted, "a full day of attempts admits exactly the quota")
	is.Equal(0, q.remaining(start.Add(23*time.Hour)))
	// Slots return one by one, exactly 24h after each admission.
	is.Equal(1, q.remaining(start.Add(24*time.Hour+time.Second)))
	is.Equal(limit, q.remaining(start.Add(24*time.Hour+3*time.Minute)))
}
