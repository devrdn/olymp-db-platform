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

func TestAdvanceContestScheduleReportsFailureAndSilenceOtherwise(t *testing.T) {
	failure := errors.New("database is away")
	var buf bytes.Buffer
	job := advanceContestSchedule(slog.New(slog.NewTextHandler(&buf, nil)), func(context.Context) (int, int, error) {
		return 0, 0, failure
	})

	if err := job.run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("run() = %v, want %v", err, failure)
	}
	if strings.Contains(buf.String(), "advanced the contest schedule") {
		t.Fatalf("a failed tick claimed to have advanced the schedule: %s", buf.String())
	}
}

func TestAdvanceContestScheduleLogsWhatMoved(t *testing.T) {
	var buf bytes.Buffer
	job := advanceContestSchedule(slog.New(slog.NewTextHandler(&buf, nil)), func(context.Context) (int, int, error) {
		return 2, 3, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "advanced the contest schedule") {
		t.Fatalf("a tick that started 2 and finished 3 contests logged nothing: %q", line)
	}
	if !strings.Contains(line, "started=2") || !strings.Contains(line, "finished=3") {
		t.Fatalf("the line does not carry both counts: %q", line)
	}

	buf.Reset()
	job = advanceContestSchedule(slog.New(slog.NewTextHandler(&buf, nil)), func(context.Context) (int, int, error) {
		return 0, 1, nil
	})
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if !strings.Contains(buf.String(), "finished=1") {
		t.Fatalf("a tick that only finished contests logged %q", buf.String())
	}
}

func TestAdvanceContestScheduleSucceedsWhenNothingMoved(t *testing.T) {
	var buf bytes.Buffer
	job := advanceContestSchedule(slog.New(slog.NewTextHandler(&buf, nil)), func(context.Context) (int, int, error) {
		return 0, 0, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v, want nil for a tick that moved nothing", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a tick that moved nothing logged %q", buf.String())
	}
}

func TestTheSweepAsksForRowsOlderThanALiveQueryCouldBe(t *testing.T) {
	var asked time.Duration
	job := sweepQueryLog(quiet(), func(_ context.Context, older time.Duration) (int64, error) {
		asked = older
		return 0, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if asked < time.Minute {
		t.Fatalf("cut-off is %s, close enough to a live query to catch one", asked)
	}
}

func TestReclaimInstancesPassesTheConfiguredGraceAndReportsFailure(t *testing.T) {
	failure := errors.New("the game cluster is away")
	var gotGrace int
	// Prometheus, not Noop, so untouched counters would show.
	recorder := metrics.NewPrometheus()
	counters := metrics.NewGameReclaimCounters(recorder)

	job := reclaimInstances(quiet(), func(_ context.Context, graceMin int) (provisioning.ReclaimResult, error) {
		gotGrace = graceMin
		return provisioning.ReclaimResult{Reclaimed: 2, Failed: 1, TemplatesReclaimed: 1}, failure
	}, 90, counters)

	if err := job.run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("run() = %v, want %v", err, failure)
	}
	if gotGrace != 90 {
		t.Fatalf("grace passed to Reclaim = %d, want 90", gotGrace)
	}
	// The tick failed, but what it dropped first must still be counted.
	totals := reclaimCounterTotals(t, recorder)
	for name, want := range map[string]float64{
		"game_instances_reclaimed_total":      2,
		"game_instances_reclaim_failed_total": 1,
		"game_templates_reclaimed_total":      1,
	} {
		if totals[name] != want {
			t.Errorf("%s = %v after a failed tick, want %v", name, totals[name], want)
		}
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

func TestReclaimInstancesReportsSkippedAndStuckWithoutError(t *testing.T) {
	var buf bytes.Buffer
	recorder := metrics.NewPrometheus()
	counters := metrics.NewGameReclaimCounters(recorder)
	contest := uuid.New()
	job := reclaimInstances(slog.New(slog.NewTextHandler(&buf, nil)), func(context.Context, int) (provisioning.ReclaimResult, error) {
		return provisioning.ReclaimResult{
			Skipped: 3,
			Stuck: []provisioning.StuckInstance{
				{Database: "game_c1_u1", ContestID: contest, Overdue: 48 * time.Hour},
			},
		}, nil
	}, 60, counters)

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v, want nil for a tick that only skipped busy databases", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "reclaimed game instances") || !strings.Contains(logged, "skipped=3") {
		t.Fatalf("a tick that skipped three databases did not say so: %q", logged)
	}
	if !strings.Contains(logged, "game_c1_u1") || stuckWarningLines(&buf) != 1 {
		t.Fatalf("the stuck database was not named in its own warning: %q", logged)
	}
	if totals := reclaimCounterTotals(t, recorder); totals["game_instances_reclaim_skipped_total"] != 3 {
		t.Fatalf("game_instances_reclaim_skipped_total = %v, want 3",
			totals["game_instances_reclaim_skipped_total"])
	}
}

// reclaimCounterTotals reads the reclaim counters through Gather, as a
// scrape would, keyed by metric name.
func reclaimCounterTotals(t *testing.T, p *metrics.Prometheus) map[string]float64 {
	t.Helper()
	families, err := p.Registry().Gather()
	if err != nil {
		t.Fatalf("gather the metrics: %v", err)
	}
	totals := map[string]float64{}
	for _, family := range families {
		for _, m := range family.GetMetric() {
			if c := m.GetCounter(); c != nil {
				totals[family.GetName()] = c.GetValue()
			}
		}
	}
	return totals
}

func stuckWarningLines(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "busy long past its grace deadline")
}

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

	// One tick more overdue, same day: no new line.
	overdue += 10 * time.Minute
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 1 {
		t.Fatalf("tick with no day crossed logged %d warnings total, want still 1", got)
	}

	overdue += 24 * time.Hour
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if got := stuckWarningLines(&buf); got != 2 {
		t.Fatalf("tick that crossed another day logged %d warnings total, want 2", got)
	}
}

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

// The interval is an hour, so only the run at startup can make this pass.
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

func TestWhichBackgroundJobsRunAtStartup(t *testing.T) {
	counters := metrics.NewGameReclaimCounters(metrics.Noop{})

	for _, tc := range []struct {
		job     task
		atStart bool
		why     string
	}{
		{
			job:     tendPools(quiet(), nil, provisioning.PoolLimits{Headroom: 5, MaxCopies: 100}, nil),
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

// The interval is an hour, so only a send on wake can make this pass.
func TestATriggerRunsTheJobBeforeItsNextTick(t *testing.T) {
	wake := make(chan struct{}, 1)
	ran := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:  "tender",
		every: time.Hour,
		wake:  wake,
		run:   func(context.Context) error { ran <- struct{}{}; return nil },
	})

	wake <- struct{}{}

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("a trigger did not run the job before its own interval")
	}
}

func TestAJobWithNoTriggerWiredIsUnaffected(t *testing.T) {
	var runs atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go runPeriodically(ctx, quiet(), task{
		name:  "no-trigger",
		every: time.Hour,
		run:   func(context.Context) error { runs.Add(1); return nil },
	})

	time.Sleep(200 * time.Millisecond)
	if got := runs.Load(); got != 0 {
		t.Fatalf("a job with no trigger and an hour-long interval ran %d times", got)
	}
}

func TestTendPoolsTicksEveryMinute(t *testing.T) {
	job := tendPools(quiet(), nil, provisioning.PoolLimits{}, nil)

	if job.every != time.Minute {
		t.Fatalf("tendPools interval = %s, want 1 minute", job.every)
	}
}

func TestTendPoolsWiresTheTendersWakeChannel(t *testing.T) {
	tender := provisioning.NewTender()
	job := tendPools(quiet(), nil, provisioning.PoolLimits{}, tender)

	tender.Trigger(uuid.New())

	select {
	case <-job.wake:
	case <-time.After(time.Second):
		t.Fatal("tendPools' task did not wire the tender's own wake channel")
	}
}

func TestTendPoolsWithNoTenderIsUnaffected(t *testing.T) {
	job := tendPools(quiet(), nil, provisioning.PoolLimits{}, nil)

	if job.wake != nil {
		t.Fatalf("wake = %v, want nil with no tender wired", job.wake)
	}
}
