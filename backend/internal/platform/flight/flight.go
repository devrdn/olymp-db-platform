// Package flight collapses concurrent computations of one value into a single
// call, for the hot reads behind a short cache (leaderboard, front page,
// monitoring roster). It does not cache: each caller keeps its own cache and
// asks this only on a miss.
package flight

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// Do runs fn once per key for all concurrent callers, who share its result.
//
// fn runs detached from the caller's context and bounded by timeout instead:
// the computation belongs to the key, so one caller giving up must not cut it
// short for the others. Each caller still stops waiting when its own ctx ends.
//
// A panic in fn is recovered into an error, because singleflight re-panics on
// an unrecovered goroutine when a call has joiners and would crash the
// process. singleflight forgets a key once its call returns, so a failure is
// never cached and the next call starts afresh.
func Do(ctx context.Context, group *singleflight.Group, key string, timeout time.Duration,
	fn func(ctx context.Context) (any, error)) (any, error) {
	ch := group.DoChan(key, func() (result any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("compute %s: %v", key, r)
			}
		}()
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return fn(flightCtx)
	})

	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
