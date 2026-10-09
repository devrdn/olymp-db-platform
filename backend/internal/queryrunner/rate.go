package queryrunner

import (
	"errors"
	"sync"
	"time"
)

// ErrTooManyQueries is a participant asking faster than the contest allows.
var ErrTooManyQueries = errors.New("too many queries")

// window is a sliding count of each participant's queries. The gate bounds
// what runs at once; this bounds a rate, which the gate cannot. It is in
// memory because one Query Runner is one process.
type window struct {
	limit int
	over  time.Duration
	now   func() time.Time

	mu    sync.Mutex
	seen  map[string][]time.Time
	since int
}

// pruneEvery is how many admissions pass between sweeps of the map.
const pruneEvery = 256

func newWindow(limit int, over time.Duration, now func() time.Time) *window {
	if now == nil {
		now = time.Now
	}
	return &window{limit: limit, over: over, now: now, seen: map[string][]time.Time{}}
}

// admit records one query and reports whether it is within the rate. A query
// refused by the rate is not recorded, so retrying does not extend the penalty
// into a lockout.
func (w *window) admit(participant string) error {
	return w.admitAt(participant, w.limit)
}

// admitAt is admit with an explicit limit, such as a contest's own.
func (w *window) admitAt(participant string, limit int) error {
	if limit <= 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	if w.since++; w.since >= pruneEvery {
		w.since = 0
		w.prune(now)
	}
	cutoff := now.Add(-w.over)

	kept := w.seen[participant][:0]
	for _, at := range w.seen[participant] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}

	if len(kept) >= limit {
		w.seen[participant] = kept
		return ErrTooManyQueries
	}

	w.seen[participant] = append(kept, now)
	return nil
}

// RateLimiter bounds how often one key may pass, in a sliding window. It lets
// a caller outside the runner, such as one enforcing a contest's configured
// limit, reuse the same algorithm.
type RateLimiter struct{ w *window }

// NewRateLimiter returns a limiter allowing defaultLimit passes of one key per
// interval. Zero means no default limit.
func NewRateLimiter(defaultLimit int, over time.Duration) *RateLimiter {
	return &RateLimiter{w: newWindow(defaultLimit, over, nil)}
}

// Admit records one pass of key and reports whether it is within limit passes
// per interval. A limit of zero or less uses the limiter's default.
func (r *RateLimiter) Admit(key string, limit int) error {
	if limit <= 0 {
		limit = r.w.limit
	}
	return r.w.admitAt(key, limit)
}

// prune drops participants with nothing left in the window, so the map does
// not keep a key for everyone who ever asked.
func (w *window) prune(now time.Time) {
	cutoff := now.Add(-w.over)
	for participant, at := range w.seen {
		if len(at) == 0 || at[len(at)-1].Before(cutoff) {
			delete(w.seen, participant)
		}
	}
}
