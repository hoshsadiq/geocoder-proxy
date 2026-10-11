package service

import (
	"sync"
	"time"
)

// quotaWindow is the budget period: a rolling 24-hour window, not a
// provider's calendar day. Capping admissions in every trailing 24h span
// caps them in any 24h span, so the provider's reset time — published or
// not, UTC or local — never needs to be configured or known. The price is
// mild conservatism: budget returns gradually (each slot exactly 24h after
// its call) instead of all at once at the provider's midnight, which is the
// safe direction for a budget.
const quotaWindow = 24 * time.Hour

// quotaCounter counts calls against a provider's daily quota over a rolling
// 24-hour window.
type quotaCounter struct {
	mu    sync.Mutex
	limit int
	calls []time.Time // admission timestamps, oldest first, len <= limit
}

func newQuotaCounter(limit int) *quotaCounter {
	return &quotaCounter{limit: limit, calls: make([]time.Time, 0, limit)}
}

// prune drops admissions older than the window. Caller holds the lock.
func (q *quotaCounter) prune(now time.Time) {
	cutoff := now.Add(-quotaWindow)
	keep := 0
	for _, t := range q.calls {
		if t.After(cutoff) {
			break
		}
		keep++
	}
	q.calls = append([]time.Time(nil), q.calls[keep:]...)
}

// admit reports whether one more call fits the quota and, if so, spends it.
// Spending before the call keeps a slow or failing call counted, which is
// the honest accounting: the provider saw the request.
func (q *quotaCounter) admit(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.prune(now)
	if len(q.calls) >= q.limit {
		return false
	}
	q.calls = append(q.calls, now)
	return true
}

// remaining reports how many calls are left in the current window.
func (q *quotaCounter) remaining(now time.Time) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.prune(now)
	return q.limit - len(q.calls)
}
