package queryrunner

import "context"

// HoldConnection lends a read connection from the runner's pool without
// passing through the gate, and returns the function that hands it back.
//
// It exists for the one proof the gate otherwise makes impossible: what the
// pool does when asked for more connections than its bound with none idle.
// Nothing else should reach past Run.
func HoldConnection(ctx context.Context, r *Runner, database string) (func(clean bool), error) {
	s, err := r.conns.acquire(ctx, database, false, int64(r.limits.MaxBytes)+readSlack)
	if err != nil {
		return nil, err
	}
	return func(clean bool) { r.conns.release(ctx, s, clean) }, nil
}
