package queryrunner

import (
	"errors"
	"sync"
	"time"
)

// ErrTooManyQueries is a participant asking faster than the contest allows.
var ErrTooManyQueries = errors.New("too many queries")

// window is a sliding count of one participant's queries.
//
// Separate from the semaphore next door because they bound different things,
// and section 5 lists them as different layers. The semaphore bounds what is
// happening *now*: it stops a hundred people running heavy queries together.
// This bounds a rate: it stops one person running a thousand cheap ones in a
// minute, each of which the semaphore would happily admit because the previous
// had already finished.
//
// In memory rather than in Redis, because one Query Runner is one process and
// the limit is per participant. A second instance would need the count shared,
// which is the same conversation as sessions and belongs with it.
type window struct {
	limit int
	over  time.Duration
	// now is injected so the tests can move time rather than spend it.
	now func() time.Time

	mu    sync.Mutex
	seen  map[string][]time.Time
	since int
}

// pruneEvery is how many admissions pass between sweeps of the map. Often
// enough that it cannot grow unboundedly, rarely enough that the sweep is not
// what the rate limiter spends its time on.
const pruneEvery = 256

func newWindow(limit int, over time.Duration, now func() time.Time) *window {
	if now == nil {
		now = time.Now
	}
	return &window{limit: limit, over: over, now: now, seen: map[string][]time.Time{}}
}

// admit records one query and reports whether it is within the rate.
//
// A refused query is not recorded. Otherwise a participant who kept clicking
// would extend their own penalty indefinitely, which turns a rate limit into a
// lockout — and the point is to slow somebody down, not to remove them from
// the contest.
func (w *window) admit(participant string) error {
	return w.admitAt(participant, w.limit)
}

// admitAt is admit with the limit made explicit, so a caller who knows a more
// specific rate than the instance's own — a contest's configured limit, in
// particular — can enforce that one against the same sliding window instead
// of a second one of its own.
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

	// Pruned on the way past, which is what keeps the map from growing with
	// every participant who ever asked anything.
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

// RateLimiter bounds how often one key may pass, in a sliding window.
//
// Exported so that a caller elsewhere in the process can enforce a rate the
// Runner does not know — a contest's own configured limit, decided by an
// organiser long before a query reaches this package — against the same
// sliding-window logic the Runner's own per-instance check uses, rather than
// a second implementation of it. The Query Runner and the façade in front of
// it are separate processes, so this is still a second *instance*; it is
// never a second *algorithm*.
type RateLimiter struct{ w *window }

// NewRateLimiter returns a limiter whose default is defaultLimit passes of
// one key per the given interval. Zero means the caller has no default of its
// own to fall back to.
func NewRateLimiter(defaultLimit int, over time.Duration) *RateLimiter {
	return &RateLimiter{w: newWindow(defaultLimit, over, nil)}
}

// Admit records one pass of key and reports whether it is within limit passes
// of the configured interval. limit of zero or less falls back to the
// limiter's own default.
func (r *RateLimiter) Admit(key string, limit int) error {
	if limit <= 0 {
		limit = r.w.limit
	}
	return r.w.admitAt(key, limit)
}

// prune drops participants with nothing left in the window.
//
// Called on the way past rather than on a timer: without it the map keeps a
// key for everybody who ever asked anything, which over a term of olympiads is
// a slow leak in a process meant to run for months.
func (w *window) prune(now time.Time) {
	cutoff := now.Add(-w.over)
	for participant, at := range w.seen {
		if len(at) == 0 || at[len(at)-1].Before(cutoff) {
			delete(w.seen, participant)
		}
	}
}
