package service

import (
	"sync"
	"time"
)

// quotaCounter counts calls against a provider's daily quota. The day
// boundary is UTC because provider quotas reset at midnight UTC.
type quotaCounter struct {
	mu    sync.Mutex
	limit int
	day   time.Time
	count int
}

func newQuotaCounter(limit int) *quotaCounter {
	return &quotaCounter{limit: limit}
}

// admit reports whether one more call fits today's quota and, if so, spends
// it. Spending before the call keeps a slow or failing call counted, which is
// the honest accounting: the provider saw the request.
func (q *quotaCounter) admit(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	utcDay := now.UTC().Truncate(24 * time.Hour)
	if !utcDay.Equal(q.day) {
		q.day = utcDay
		q.count = 0
	}
	if q.count >= q.limit {
		return false
	}
	q.count++
	return true
}

// remaining reports how many calls are left today.
func (q *quotaCounter) remaining(now time.Time) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !now.UTC().Truncate(24 * time.Hour).Equal(q.day) {
		return q.limit
	}
	return q.limit - q.count
}
