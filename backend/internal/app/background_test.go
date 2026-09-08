package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
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

// reclaimInstances wraps provisioning.Service.Reclaim into a task; Reclaim's
// own rules (the grace period, the busy-database skip, one failure not
// stopping the rest) are exercised where they are declared
// (internal/provisioning/reclaim_test.go), so this only has to prove the
// wrapping: the configured grace reaches the call, a failure is reported
// rather than swallowed, and every count reaches the metrics pair regardless.
func TestReclaimInstancesPassesTheConfiguredGraceAndReportsFailure(t *testing.T) {
	failure := errors.New("the game cluster is away")
	var gotGrace int
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})

	job := reclaimInstances(quiet(), func(_ context.Context, graceMin int) (provisioning.ReclaimResult, error) {
		gotGrace = graceMin
		return provisioning.ReclaimResult{Reclaimed: 2, Failed: 1}, failure
	}, 90, counters)

	if err := job.run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("run() = %v, want %v", err, failure)
	}
	if gotGrace != 90 {
		t.Fatalf("grace passed to Reclaim = %d, want 90", gotGrace)
	}
}

func TestReclaimInstancesSucceedsWhenNothingWasThere(t *testing.T) {
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})
	job := reclaimInstances(quiet(), func(context.Context, int) (provisioning.ReclaimResult, error) {
		return provisioning.ReclaimResult{}, nil
	}, 60, counters)

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v, want nil for a tick that reclaimed nothing", err)
	}
}

// A tick that only skipped busy databases used to report "reclaimed=0
// failed=0", indistinguishable from nothing being due at all. This proves
// the wrapping surfaces Skipped (and a Stuck entry) without erroring, so an
// operator reading the log — or the counters underneath it — can tell the
// two apart.
func TestReclaimInstancesReportsSkippedAndStuckWithoutError(t *testing.T) {
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})
	job := reclaimInstances(quiet(), func(context.Context, int) (provisioning.ReclaimResult, error) {
		return provisioning.ReclaimResult{
			Skipped: 3,
			Stuck: []provisioning.StuckInstance{
				{Database: "game_c1_u1", ContestID: uuid.New(), Overdue: 48 * time.Hour},
			},
		}, nil
	}, 60, counters)

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v, want nil for a tick that only skipped busy databases", err)
	}
}

// stuckWarningLines counts the "busy long past its grace deadline" warning
// lines logged so far — the fact finding 3 asks about: one line the first
// time a database becomes stuck, silence on every following tick where
// nothing about it changed.
func stuckWarningLines(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "busy long past its grace deadline")
}

// A database stuck at the same overdue duration tick after tick — the
// ordinary steady state for as long as something stays connected to it — must
// warn once, not once every ten minutes (144 lines a day per database before
// this fix). Crossing another full day overdue is a real change and gets its
// own line.
func TestReclaimInstancesWarnsOnceThenOnlyWhenAnotherDayPasses(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})
	contest := uuid.New()

	overdue := 30 * time.Hour // already past stuckAfter (24h), one day in
	job := reclaimInstances(log, func(context.Context, int) (provisioning.ReclaimResult, error) {
		return provisioning.ReclaimResult{
			Stuck: []provisioning.StuckInstance{{Database: "game_c1_u1", ContestID: contest, Overdue: overdue}},
		}, nil
	}, 60, counters)

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 1 {
		t.Fatalf("first tick logged %d warnings, want 1", got)
	}

	// Still the same database, still stuck, ten minutes (one tick) more
	// overdue — the same day, so no new line.
	overdue += 10 * time.Minute
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 1 {
		t.Fatalf("tick with no day crossed logged %d warnings total, want still 1", got)
	}

	// Now a full day further overdue — worth its own line.
	overdue += 24 * time.Hour
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 2 {
		t.Fatalf("tick that crossed another day logged %d warnings total, want 2", got)
	}
}

// A database that stops being stuck (reclaimed, or simply freed up) and later
// gets stuck again is a new fact, not a continuation of the old one — it must
// warn again rather than staying silent because this process warned about the
// same database name once before.
func TestReclaimInstancesWarnsAgainAfterRecoveringAndGettingStuckAgain(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})
	contest := uuid.New()
	stuck := true

	job := reclaimInstances(log, func(context.Context, int) (provisioning.ReclaimResult, error) {
		if !stuck {
			return provisioning.ReclaimResult{}, nil
		}
		return provisioning.ReclaimResult{
			Stuck: []provisioning.StuckInstance{{Database: "game_c1_u1", ContestID: contest, Overdue: 30 * time.Hour}},
		}, nil
	}, 60, counters)

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 1 {
		t.Fatalf("first tick logged %d warnings, want 1", got)
	}

	stuck = false
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}

	stuck = true
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 2 {
		t.Fatalf("after recovering and getting stuck again, warnings = %d, want 2", got)
	}
}

// A pool tender that waits out its own ten-minute interval before it does
// anything is a pool nobody tended for ten minutes — and an API restarted
// five minutes before a contest opens leaves every participant waiting for
// CREATE DATABASE inside their own page load.
//
// The interval here is an hour, so nothing but the run at startup can make
// this pass.
func TestAJobMarkedAtStartRunsBeforeItsFirstTick(t *testing.T) {
	ran := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:    "tender",
		every:   time.Hour,
		atStart: true,
		run:     func(context.Context) error { ran <- struct{}{}; return nil },
	})

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the job did not run at startup; it is waiting out its whole interval first")
	}
}

// The opposite claim, and the one that makes atStart a per-job decision
// rather than a global change: a job that did not ask for it stays on its
// tick. game-reclaim is the job this protects — its tick drops databases.
func TestAJobNotMarkedAtStartWaitsForItsFirstTick(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:  "reclaimer",
		every: time.Hour,
		run:   func(context.Context) error { runs.Add(1); return nil },
	})

	time.Sleep(200 * time.Millisecond)
	if got := runs.Load(); got != 0 {
		t.Fatalf("a job with no atStart ran %d times before its first tick", got)
	}
}

// A run at startup that failed must not stop the schedule, for the same
// reason a failed tick does not: the database being briefly away is not a
// reason to leave the pool untended for the rest of the process's life.
func TestAJobThatFailsAtStartupKeepsItsSchedule(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:    "always fails",
		every:   10 * time.Millisecond,
		atStart: true,
		run:     func(context.Context) error { runs.Add(1); return errors.New("nope") },
	})

	deadline := time.After(2 * time.Second)
	for runs.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("ran %d times; a failure at startup stopped the schedule", runs.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// Which jobs run at startup is the decision finding 3 is about, and it is a
// decision per job — so it is asserted per job, here, rather than left to
// whoever next reads the constructors. The reasoning for each is in its own
// constructor in background.go.
func TestWhichBackgroundJobsRunAtStartup(t *testing.T) {
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})

	for _, tc := range []struct {
		job     task
		atStart bool
		why     string
	}{
		{
			job:     tendPools(quiet(), nil, provisioning.PoolLimits{Headroom: 5, MaxCopies: 100}),
			atStart: true,
			why:     "a pool left untended for ten minutes is every participant waiting for CREATE DATABASE",
		},
		{
			job: sweepQueryLog(quiet(), func(context.Context, time.Duration) (int64, error) {
				return 0, nil
			}),
			atStart: true,
			why:     "the rows it closes were left open by the process this one replaced",
		},
		{
			job: advanceContestSchedule(quiet(), func(context.Context) (int, int, error) {
				return 0, 0, nil
			}),
			atStart: true,
			why:     "a participant's own screen waits on this the moment a contest's window opens",
		},
		{
			job: buildGames(quiet(), func(context.Context, time.Duration) (provisioning.Template, error) {
				return provisioning.Template{}, provisioning.ErrNoGame
			}, gamedb.DefaultBuildTimeout),
			atStart: true,
			why:     "a game left in `building` by a dead process is recovered only by a tick of this",
		},
		{
			job: reclaimInstances(quiet(), func(context.Context, int) (provisioning.ReclaimResult, error) {
				return provisioning.ReclaimResult{}, nil
			}, 60, counters),
			atStart: false,
			why:     "it drops databases, nothing waits on it, and a grace period bounds what it can find",
		},
	} {
		t.Run(tc.job.name, func(t *testing.T) {
			if tc.job.atStart != tc.atStart {
				t.Fatalf("%s: atStart = %v, want %v — %s", tc.job.name, tc.job.atStart, tc.atStart, tc.why)
			}
		})
	}
}

// The cut-off after which a game still at `building` is taken to belong to a
// process that died has to outlast the budget the build itself was given.
// Fifteen minutes was a constant beside a thirty-minute, deployment-settable
// GAME_BUILD_TIMEOUT, so a build of a multi-gigabyte dump was claimed a second
// time while the first was still streaming — and the second BuildTemplate
// begins by dropping the template the first is filling. See staleBuildAfter
// for what that costs and for why the number is derived rather than declared.
func TestTheStaleBuildCutOffOutlastsWhateverBudgetABuildWasGiven(t *testing.T) {
	for _, budget := range []time.Duration{time.Minute, gamedb.DefaultBuildTimeout, 4 * time.Hour} {
		var asked time.Duration
		job := buildGames(quiet(), func(_ context.Context, stale time.Duration) (provisioning.Template, error) {
			asked = stale
			return provisioning.Template{}, provisioning.ErrNoGame
		}, budget)

		if err := job.run(t.Context()); err != nil {
			t.Fatalf("GAME_BUILD_TIMEOUT=%s: run() = %v", budget, err)
		}
		if asked <= budget {
			t.Errorf("GAME_BUILD_TIMEOUT=%s: a build is reclaimed after %s, "+
				"so one still inside its own budget is claimed a second time", budget, asked)
		}
	}
}
