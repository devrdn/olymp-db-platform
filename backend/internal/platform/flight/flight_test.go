package flight

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

// TestConcurrentCallersShareOneComputation is the reason the package exists:
// callers arriving while a value is being computed wait for it rather than
// starting one each.
func TestConcurrentCallersShareOneComputation(t *testing.T) {
	var group singleflight.Group
	var calls atomic.Int32
	release := make(chan struct{})

	const callers = 20
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	for range callers {
		go func() {
			defer done.Done()
			started.Done()
			got, err := Do(t.Context(), &group, "key", time.Second, func(context.Context) (any, error) {
				calls.Add(1)
				<-release
				return 42, nil
			})
			if err != nil || got != 42 {
				t.Errorf("Do = %v, %v; want 42, nil", got, err)
			}
		}()
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond) // let the callers reach the flight
	close(release)
	done.Wait()

	if n := calls.Load(); n >= callers {
		t.Fatalf("fn ran %d times for %d concurrent callers; they should have shared it", n, callers)
	}
}

// TestAPanicBecomesAnErrorAndIsNotRemembered: one bad computation is a
// refusal, not a crash, and the next call for the key starts afresh.
func TestAPanicBecomesAnErrorAndIsNotRemembered(t *testing.T) {
	var group singleflight.Group
	_, err := Do(t.Context(), &group, "key", time.Second, func(context.Context) (any, error) {
		panic("boom")
	})
	if err == nil {
		t.Fatal("a panicking computation reported no error")
	}

	got, err := Do(t.Context(), &group, "key", time.Second, func(context.Context) (any, error) {
		return "fresh", nil
	})
	if err != nil || got != "fresh" {
		t.Fatalf("the call after a panic = %v, %v; want a fresh computation", got, err)
	}
}

// TestACallerWhoGoesAwayStopsWaitingWithoutCancellingTheComputation: the
// computation belongs to the key, not to the caller that happened to start it.
func TestACallerWhoGoesAwayStopsWaitingWithoutCancellingTheComputation(t *testing.T) {
	var group singleflight.Group
	finished := make(chan error, 1)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Do(ctx, &group, "key", time.Second, func(ctx context.Context) (any, error) {
		time.Sleep(20 * time.Millisecond)
		finished <- ctx.Err()
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller whose context ended got %v, want context.Canceled", err)
	}
	if ctxErr := <-finished; ctxErr != nil {
		t.Fatalf("the computation saw its context end (%v); it must outlive the caller that started it", ctxErr)
	}
}
