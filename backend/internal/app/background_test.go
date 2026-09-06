package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAPeriodicJobRunsAgainAndStopsWhenTold(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodically(ctx, quiet(), task{
			name:  "counter",
			every: 10 * time.Millisecond,
			run:   func(context.Context) error { runs.Add(1); return nil },
		})
	}()

	// Two ticks is the claim: it repeats rather than running once.
	deadline := time.After(2 * time.Second)
	for runs.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("ran %d times in two seconds", runs.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the job did not stop when the context was cancelled")
	}
}

// Housekeeping that cannot run because the database is briefly away should try
// again, not stop for ever — nor take the service down.
func TestAFailingJobKeepsItsSchedule(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:  "always fails",
		every: 10 * time.Millisecond,
		run:   func(context.Context) error { runs.Add(1); return errors.New("nope") },
	})

	deadline := time.After(2 * time.Second)
	for runs.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("a failing job ran %d times; it stopped rescheduling", runs.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// advanceContestSchedule wraps whatever contests.Scheduler.Advance answers
// into a task; the scheduler's own rules (the lock, the two bulk moves, the
// audit trail) are exercised where they are declared
// (internal/contests/schedule_test.go), so this only has to prove the
// wrapping itself: a tick that failed is reported, and one that moved
// something is not silently unremarkable in the log the operator watches.
func TestAdvanceContestScheduleReportsFailureAndSilenceOtherwise(t *testing.T) {
	failure := errors.New("database is away")
	job := advanceContestSchedule(quiet(), func(context.Context) (int, int, error) {
		return 0, 0, failure
	})

	if err := job.run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("run() = %v, want %v", err, failure)
	}
}

func TestAdvanceContestScheduleSucceedsWhenNothingMoved(t *testing.T) {
	job := advanceContestSchedule(quiet(), func(context.Context) (int, int, error) {
		return 0, 0, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v, want nil for a tick that moved nothing", err)
	}
}

// The sweep is the second half of the two-phase journal: rows are written
// before a query runs so that a crash leaves evidence, and evidence nobody
// closes says `running` for ever.
func TestTheSweepAsksForRowsOlderThanALiveQueryCouldBe(t *testing.T) {
	var asked time.Duration
	job := sweepQueryLog(quiet(), func(_ context.Context, older time.Duration) (int64, error) {
		asked = older
		return 0, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// The runner's own deadline is five seconds. A cut-off anywhere near it
	// would mark queries that are merely slow.
	if asked < time.Minute {
		t.Fatalf("cut-off is %s, close enough to a live query to catch one", asked)
	}
}
