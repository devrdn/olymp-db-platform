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

	mu   sync.Mutex
	seen map[string][]time.Time
}

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
	if w.limit <= 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	cutoff := now.Add(-w.over)

	// Pruned on the way past, which is what keeps the map from growing with
	// every participant who ever asked anything.
	kept := w.seen[participant][:0]
	for _, at := range w.seen[participant] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}

	if len(kept) >= w.limit {
		w.seen[participant] = kept
		return ErrTooManyQueries
	}

	w.seen[participant] = append(kept, now)
	return nil
}

// forget drops a participant with nothing in the window, so that a contest
// that has finished does not stay in memory until the process restarts.
func (w *window) forget(participant string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.seen[participant]) == 0 {
		delete(w.seen, participant)
	}
}
