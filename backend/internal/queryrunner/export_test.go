package queryrunner

import "context"

// HoldConnection lends a pooled read connection past the gate, so a test can
// reach the pool's bound with nothing idle. Nothing else should bypass Run.
func HoldConnection(ctx context.Context, r *Runner, database string) (func(clean bool), error) {
	s, err := r.conns.acquire(ctx, database, false, int64(r.limits.MaxBytes)+readSlack)
	if err != nil {
		return nil, err
	}
	return func(clean bool) { r.conns.release(ctx, s, clean) }, nil
}
