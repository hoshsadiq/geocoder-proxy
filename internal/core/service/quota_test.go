package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestQuotaCounter(t *testing.T) {
	is := assert.New(t)
	day1 := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	q := newQuotaCounter(2)

	is.True(q.admit(day1))
	is.Equal(1, q.remaining(day1))
	is.True(q.admit(day1))
	is.False(q.admit(day1), "the third call on day 1 is over quota")
	is.Equal(0, q.remaining(day1))

	// The counter resets on the next UTC day.
	day2 := day1.Add(2 * time.Hour)
	is.True(q.admit(day2), "a new UTC day resets the quota")
	is.Equal(1, q.remaining(day2))
}
