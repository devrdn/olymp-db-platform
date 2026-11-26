package queryrunner

import (
	"context"
	"math"
	"sync"
	"time"
)

// HoldConnection lends a pooled read connection past the gate, so a test can
// reach the pool's bound with nothing idle. Nothing else should bypass Run.
func HoldConnection(ctx context.Context, r *Runner, database string) (func(clean bool), error) {
	s, err := r.conns.acquire(ctx, database, false, int64(r.limits.MaxBytes)+readSlack)
	if err != nil {
		return nil, err
	}
	return func(clean bool) { r.conns.release(ctx, s, clean) }, nil
}

// HeldTimeout is one idle timeout the pool armed and a test holds.
type HeldTimeout struct {
	After  time.Duration
	expire func()
}

// RunOut does what the timer would on running out: it closes the connection,
// unless it has been lent out or closed since.
func (h HeldTimeout) RunOut() { h.expire() }

// HoldIdleTimeouts stops the runner's idle timeouts from running out on their
// own, and returns a function listing those armed since, in order.
func HoldIdleTimeouts(r *Runner) func() []HeldTimeout {
	var mu sync.Mutex
	var armed []HeldTimeout
	r.conns.mu.Lock()
	r.conns.afterFunc = func(after time.Duration, expire func()) *time.Timer {
		mu.Lock()
		armed = append(armed, HeldTimeout{After: after, expire: expire})
		mu.Unlock()
		return time.NewTimer(math.MaxInt64)
	}
	r.conns.mu.Unlock()
	return func() []HeldTimeout {
		mu.Lock()
		defer mu.Unlock()
		return append([]HeldTimeout(nil), armed...)
	}
}

// Ending exposes the classification Run reports, so every error it can meet
// is checked against Endings without staging each one on a cluster.
var Ending = ending
