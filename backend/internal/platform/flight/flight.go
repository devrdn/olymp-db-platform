// Package flight collapses concurrent computations of one value into a single
// call — the hot reads behind a short cache: the leaderboard, the front page's
// numbers, the monitoring roster.
//
// It answers "how do N simultaneous cache misses become one computation". It
// does not cache: every caller keeps its own cache, with its own lifetime and
// its own invalidation, and asks this only on a miss.
package flight

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// Do collapses every concurrent call for one key into a single call to fn, so
// however many callers arrive while a value is being computed, it is computed
// once and all of them read the same answer.
//
// fn runs detached from any one caller's context (context.WithoutCancel),
// bounded instead by timeout: the computation belongs to the key, not to
// whichever caller's request happened to start it, so one caller giving up
// must not cut short an answer the callers behind it are still waiting for.
// Each caller still honours its own context — it stops waiting the moment ctx
// is done, without touching the flight it joined.
//
// A panic inside fn is recovered into an error rather than left to
// singleflight's own handling, which — when a call has joiners — re-panics on
// a fresh, unrecovered goroutine specifically so the crash cannot be
// swallowed. That is the right choice for a bug an operator must see, but the
// wrong one for a single bad computation to cost the whole process; recovery
// here turns it into an ordinary refusal instead. Either way, singleflight
// forgets a key the moment its call returns — before any of it is reported
// back — so a failed or recovered computation is never cached, and the very
// next call for the same key starts a fresh one rather than waiting behind a
// key that could never resolve.
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
