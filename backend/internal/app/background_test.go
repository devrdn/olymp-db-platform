package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/covers"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamedb"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/filestore"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/metrics"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/retention"
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
	totals := seriesValues(t, recorder)
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
	if totals := seriesValues(t, recorder); totals["game_instances_reclaim_skipped_total"] != 3 {
		t.Fatalf("game_instances_reclaim_skipped_total = %v, want 3",
			totals["game_instances_reclaim_skipped_total"])
	}
}

// seriesValues reads every counter and gauge p exports through Gather, as a
// scrape would, keyed by series name.
func seriesValues(t *testing.T, p *metrics.Prometheus) map[string]float64 {
	t.Helper()
	families, err := p.Registry().Gather()
	if err != nil {
		t.Fatalf("gather the metrics: %v", err)
	}
	values := map[string]float64{}
	for _, family := range families {
		for _, m := range family.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				values[family.GetName()] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				values[family.GetName()] = m.GetGauge().GetValue()
			}
		}
	}
	return values
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
			job:     deliverMail(func(context.Context) error { return nil }, nil),
			atStart: true,
			why:     "messages queued before a restart, sign-in details among them, wait on its first run",
		},
		{
			job:     expireMail(func(context.Context) error { return nil }),
			atStart: true,
			why:     "with mail off it is all that erases one-time passwords queued before, and a boot is when mail was turned off",
		},
		{
			job: reclaimInstances(quiet(), func(context.Context, int) (provisioning.ReclaimResult, error) {
				return provisioning.ReclaimResult{}, nil
			}, 60, counters),
			atStart: false,
			why:     "it drops databases, nothing waits on it, and a grace period bounds what it can find",
		},
		{
			job: watchQuotas(quiet(), func(context.Context) (provisioning.QuotaSweep, error) {
				return provisioning.QuotaSweep{}, nil
			}, metrics.NewGameQuotaMetrics(metrics.Noop{}, 0), quotaWatchTimeout),
			atStart: false,
			why:     "it only reports, the Query Runner enforces the quota on its own, and a boot-time sweep would stat every database during the restart",
		},
		{
			job: countActiveParticipants(func(context.Context, time.Time) (int, error) { return 0, nil },
				metrics.NewParticipantGauge(metrics.Noop{})),
			atStart: true,
			why:     "a dashboard opened after a restart would read zero participants until the first tick",
		},
		{
			job:     pruneRetention(quiet(), nil, metrics.NewRetentionCounters(metrics.Noop{}, nil)),
			atStart: false,
			why:     "it deletes history nobody waits on, and a boot-time delete would compete with the restart for the core pool",
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

// coverRows is the covers table as the sweep reads it.
type coverRows []string

func (r coverRows) ReferencedHashes(context.Context) ([]string, error) { return r, nil }

func TestTheCoverSweepRemovesWhatNoContestNamesOnItsOwn(t *testing.T) {
	files, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.New() = %v", err)
	}
	kept, orphan := strings.Repeat("a", 64), strings.Repeat("b", 64)
	longAgo := time.Now().Add(-2 * covers.OrphanGrace)
	for _, hash := range []string{kept, orphan} {
		key := covers.Key(hash, 800)
		if err := files.Put(t.Context(), key, "image/jpeg", []byte("jpeg")); err != nil {
			t.Fatalf("Put(%s) = %v", key, err)
		}
		if err := os.Chtimes(filepath.Join(files.Dir(), key), longAgo, longAgo); err != nil {
			t.Fatalf("age %s: %v", key, err)
		}
	}

	job := sweepCoverOrphans(quiet(), covers.NewOrphanSweeper(files, coverRows{kept}))
	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}

	if _, _, err := files.Get(t.Context(), covers.Key(orphan, 800)); err == nil {
		t.Error("a cover file no contest names is still on the volume")
	}
	if _, _, err := files.Get(t.Context(), covers.Key(kept, 800)); err != nil {
		t.Errorf("the named cover was removed: %v", err)
	}
	if job.every != abandonedUploadsEvery || job.atStart {
		t.Errorf("every = %s, atStart = %v; want the upload janitor's %s, not at startup",
			job.every, job.atStart, abandonedUploadsEvery)
	}
}

func TestTheSignupSweepRemovesWhatExpiredByTheTick(t *testing.T) {
	var asked time.Time
	before := time.Now()
	job := sweepSignups(quiet(), func(_ context.Context, now time.Time) (int64, error) {
		asked = now
		return 3, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if asked.Before(before) || asked.After(time.Now()) {
		t.Errorf("swept as of %v, want the tick's own time", asked)
	}
	if job.every != 10*time.Minute || job.atStart {
		t.Errorf("every = %s, atStart = %v; want ten minutes, not at startup: an expired row is already ignored",
			job.every, job.atStart)
	}

	failure := errors.New("the database is away")
	failing := sweepSignups(quiet(), func(context.Context, time.Time) (int64, error) { return 0, failure })
	if err := failing.run(t.Context()); !errors.Is(err, failure) {
		t.Errorf("run() = %v, want the sweep's failure", err)
	}
}

func TestThePasswordResetSweepRemovesWhatExpiredByTheTick(t *testing.T) {
	var asked time.Time
	before := time.Now()
	job := sweepPasswordResets(quiet(), func(_ context.Context, now time.Time) (int64, error) {
		asked = now
		return 2, nil
	})

	if err := job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if asked.Before(before) || asked.After(time.Now()) {
		t.Errorf("swept as of %v, want the tick's own time", asked)
	}
	if job.name == "signup-sweep" || job.every != 10*time.Minute || job.atStart {
		t.Errorf("name = %q, every = %s, atStart = %v; want its own name, ten minutes, not at startup: an expired link already opens nothing",
			job.name, job.every, job.atStart)
	}

	failure := errors.New("the database is away")
	failing := sweepPasswordResets(quiet(), func(context.Context, time.Time) (int64, error) { return 0, failure })
	if err := failing.run(t.Context()); !errors.Is(err, failure) {
		t.Errorf("run() = %v, want the sweep's failure", err)
	}
}

func TestStoppingWorkersGivesEachTheShutdownDeadlineAndLogsOneThatOverstays(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	var stopped []string
	workers := []worker{
		{name: "slow", stop: func(ctx context.Context) error {
			if got, _ := ctx.Deadline(); !got.Equal(want) {
				t.Errorf("slow was given deadline %v, want the shutdown's %v", got, want)
			}
			stopped = append(stopped, "slow")
			return context.DeadlineExceeded
		}},
		{name: "quick", stop: func(context.Context) error {
			stopped = append(stopped, "quick")
			return nil
		}},
	}

	stopWorkers(ctx, log, workers)

	if strings.Join(stopped, ",") != "slow,quick" {
		t.Errorf("stopped %v, want every worker, the one that overstayed included", stopped)
	}
	if out := buf.String(); !strings.Contains(out, "worker=slow") || strings.Contains(out, "worker=quick") {
		t.Errorf("log = %q, want only the worker that overstayed", out)
	}
}

var errAway = errors.New("the database is away")

func retentionTotals(t *testing.T, recorder *metrics.Prometheus, name string) map[string]float64 {
	t.Helper()

	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]float64{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			totals[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	return totals
}

func TestPruneRetentionCountsEachTableAndReportsFailure(t *testing.T) {
	recorder := metrics.NewPrometheus()
	counters := metrics.NewRetentionCounters(recorder, retention.TableNames())
	job := pruneRetention(quiet(), func(context.Context) ([]retention.Outcome, error) {
		return []retention.Outcome{
			{Table: retention.TableQueryLog, Deleted: 10012},
			{Table: retention.TableAuditLog, Err: errAway},
			{Table: retention.TableMailOutbox, Deleted: 3},
		}, errAway
	}, counters)

	if err := job.run(t.Context()); !errors.Is(err, errAway) {
		t.Fatalf("run() = %v, want %v", err, errAway)
	}

	deleted := retentionTotals(t, recorder, "retention_rows_deleted_total")
	if deleted["query_log"] != 10012 || deleted["mail_outbox"] != 3 || deleted["audit_log"] != 0 {
		t.Errorf("rows deleted = %v, want query_log 10012, mail_outbox 3, audit_log 0", deleted)
	}
	failures := retentionTotals(t, recorder, "retention_failures_total")
	if failures["audit_log"] != 1 || failures["query_log"] != 0 || failures["mail_outbox"] != 0 {
		t.Errorf("failures = %v, want audit_log 1 and the rest 0", failures)
	}
}

// A shutdown mid-tick is not the table's fault, so no failure is counted; the
// rows the finished tables deleted still are.
func TestPruneRetentionCountsNoFailureForATickTheShutdownEnded(t *testing.T) {
	recorder := metrics.NewPrometheus()
	counters := metrics.NewRetentionCounters(recorder, retention.TableNames())
	ctx, cancel := context.WithCancel(t.Context())
	job := pruneRetention(quiet(), func(context.Context) ([]retention.Outcome, error) {
		cancel()
		return []retention.Outcome{
			{Table: retention.TableQueryLog, Deleted: 7},
			{Table: retention.TableAuditLog, Err: ctx.Err()},
			{Table: retention.TableMailOutbox, Err: ctx.Err()},
		}, ctx.Err()
	}, counters)

	if err := job.run(ctx); err != nil {
		t.Fatalf("run() = %v, want nil so the shutdown logs no error", err)
	}

	if got := retentionTotals(t, recorder, "retention_rows_deleted_total")["query_log"]; got != 7 {
		t.Errorf("query_log rows deleted = %v, want 7", got)
	}
	for table, n := range retentionTotals(t, recorder, "retention_failures_total") {
		if n != 0 {
			t.Errorf("%s failures = %v after a shutdown, want 0", table, n)
		}
	}
}

func TestPruneRetentionLogsOnlyATickThatDeletedSomething(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	counters := metrics.NewRetentionCounters(metrics.Noop{}, retention.TableNames())
	deleted := 0
	job := pruneRetention(log, func(context.Context) ([]retention.Outcome, error) {
		return []retention.Outcome{{Table: retention.TableQueryLog, Deleted: deleted}}, nil
	}, counters)

	if err := job.run(t.Context()); err != nil || buf.Len() != 0 {
		t.Fatalf("an idle tick = %v, logged %q; want nil and nothing", err, buf.String())
	}
	deleted = 4
	if err := job.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "deleted rows past their retention") || !strings.Contains(got, "query_log=4") {
		t.Errorf("log = %q, want the deleted rows per table", got)
	}
}

func TestPruneRetentionRunsEveryFifteenMinutes(t *testing.T) {
	job := pruneRetention(quiet(), nil, metrics.NewRetentionCounters(metrics.Noop{}, nil))
	if job.every != 15*time.Minute || job.name != "retention" {
		t.Errorf("name = %q, every = %s; want retention every 15m", job.name, job.every)
	}
}

func TestTheRetentionSweepSkipsATickWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	ran, released := 0, 0
	prune := func(context.Context) ([]retention.Outcome, error) { ran++; return nil, nil }
	otherHolds := false
	lock := func(context.Context) (func(), bool, error) {
		if otherHolds {
			return nil, false, nil
		}
		return func() { released++ }, true, nil
	}
	sweep := underRetentionLock(lock, prune)

	if _, err := sweep(t.Context()); err != nil || ran != 1 || released != 1 {
		t.Fatalf("with the lock free: err = %v, ran = %d, released = %d; want nil, 1, 1", err, ran, released)
	}
	otherHolds = true
	if _, err := sweep(t.Context()); err != nil || ran != 1 {
		t.Fatalf("with the lock taken: err = %v, ran = %d; want nil and no second run", err, ran)
	}
}

func TestTheRetentionSweepCountsEveryTableFailedWhenTheLockCannotBeAsked(t *testing.T) {
	ran := false
	recorder := metrics.NewPrometheus()
	job := pruneRetention(quiet(), underRetentionLock(
		func(context.Context) (func(), bool, error) { return nil, false, errAway },
		func(context.Context) ([]retention.Outcome, error) { ran = true; return nil, nil }),
		metrics.NewRetentionCounters(recorder, retention.TableNames()))

	if err := job.run(t.Context()); !errors.Is(err, errAway) || ran {
		t.Errorf("err = %v, ran = %v; want the lock's failure and no run", err, ran)
	}
	failures := retentionTotals(t, recorder, "retention_failures_total")
	for _, table := range retention.TableNames() {
		if failures[table] != 1 {
			t.Errorf("%s failures = %v, want 1: an outage at the lock must reach the alert", table, failures[table])
		}
	}
}

// The DSN's pool_max_conns wins over CORE_DB_POOL_MAX, so the refusal has to
// read the size of the pool as built, not the setting config checked.
func TestTheRetentionSweepRefusesACorePoolOfOneConnectionFromTheDSN(t *testing.T) {
	cfg, err := storage.PoolConfigWithMaxConns("postgres://olymp@localhost/olymp_test?pool_max_conns=1", 25)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 1 {
		t.Fatalf("MaxConns = %d, want the DSN's 1 over the configured 25", cfg.MaxConns)
	}
	err = requireRetentionPool(cfg.MaxConns)
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Errorf("requireRetentionPool(1) = %v, want a refusal that says the pool is too small", err)
	}

	if err := requireRetentionPool(2); err != nil {
		t.Errorf("requireRetentionPool(2) = %v, want it accepted", err)
	}
}

func TestTheRetentionSweepReleasesTheLockWhenItsPassFails(t *testing.T) {
	released := false
	sweep := underRetentionLock(func(context.Context) (func(), bool, error) { return func() { released = true }, true, nil },
		func(context.Context) ([]retention.Outcome, error) { return nil, errAway })

	if _, err := sweep(t.Context()); !errors.Is(err, errAway) || !released {
		t.Errorf("err = %v, released = %v; want the failure and a release", err, released)
	}
}

func TestCountActiveParticipantsSetsTheGauge(t *testing.T) {
	rec := metrics.NewPrometheus()
	job := countActiveParticipants(
		func(context.Context, time.Time) (int, error) { return 7, nil },
		metrics.NewParticipantGauge(rec))

	if err := job.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := seriesValues(t, rec)["contest_active_participants"]; got != 7 {
		t.Errorf("gauge = %v, want 7", got)
	}
}

func TestCountActiveParticipantsKeepsTheLastValueWhenTheCountFails(t *testing.T) {
	rec := metrics.NewPrometheus()
	var fail bool
	job := countActiveParticipants(func(context.Context, time.Time) (int, error) {
		if fail {
			return 0, errAway
		}
		return 7, nil
	}, metrics.NewParticipantGauge(rec))

	if err := job.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := job.run(t.Context()); !errors.Is(err, errAway) {
		t.Fatalf("run() = %v, want the count's failure", err)
	}
	if got := seriesValues(t, rec)["contest_active_participants"]; got != 7 {
		t.Errorf("gauge = %v after a failed count, want the last value 7", got)
	}
}

func TestCountActiveParticipantsRunsEveryThirtySeconds(t *testing.T) {
	job := countActiveParticipants(func(context.Context, time.Time) (int, error) { return 0, nil },
		metrics.NewParticipantGauge(metrics.Noop{}))

	if job.every != activeParticipantsEvery || activeParticipantsEvery != 30*time.Second {
		t.Errorf("every = %s, constant = %s, want 30s", job.every, activeParticipantsEvery)
	}
}

func TestNoCountTaskIsScheduledWhereNobodyReadsTheGauge(t *testing.T) {
	count := func(context.Context, time.Time) (int, error) { return 0, nil }

	if jobs := participantJobs(count, metrics.NewParticipantGauge(metrics.Noop{})); len(jobs) != 0 {
		t.Errorf("a non-Prometheus recorder got %d count tasks, want none", len(jobs))
	}
	jobs := participantJobs(count, metrics.NewParticipantGauge(metrics.NewPrometheus()))
	if len(jobs) != 1 || jobs[0].name != "active-participants" {
		t.Errorf("a Prometheus recorder got %v, want the one count task", jobs)
	}
}

func TestWatchQuotasTicksEveryMinute(t *testing.T) {
	job := watchQuotas(quiet(), nil, metrics.NewGameQuotaMetrics(metrics.Noop{}, 0), quotaWatchTimeout)

	if job.every != time.Minute {
		t.Fatalf("watchQuotas interval = %s, want 1 minute", job.every)
	}
}

// Well under the interval, so a hung tick is over before the next is due.
func TestAQuotaTicksDeadlineIsWithinHalfItsInterval(t *testing.T) {
	job := watchQuotas(quiet(), nil, metrics.NewGameQuotaMetrics(metrics.Noop{}, 0), quotaWatchTimeout)

	if quotaWatchTimeout <= 0 || quotaWatchTimeout > job.every/2 {
		t.Fatalf("quotaWatchTimeout = %s, want a deadline within half the %s interval", quotaWatchTimeout, job.every)
	}
}

// A game cluster that stops answering would otherwise hold the tick for the
// maintenance pool's ten-minute statement timeout, the monitor silent; the
// tick's own deadline turns it into a counted failure.
func TestAHungQuotaTickIsCutAtItsDeadlineAndCounted(t *testing.T) {
	recorder := metrics.NewPrometheus()
	quota := metrics.NewGameQuotaMetrics(recorder, 0)
	job := watchQuotas(quiet(), func(ctx context.Context) (provisioning.QuotaSweep, error) {
		<-ctx.Done()
		return provisioning.QuotaSweep{}, ctx.Err()
	}, quota, 50*time.Millisecond)

	started := time.Now()
	if err := job.run(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run() = %v, want the tick's deadline", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the tick ran %s past a 50 ms deadline", took)
	}
	if got := seriesValues(t, recorder)["game_quota_watch_failed_total"]; got != 1 {
		t.Fatalf("game_quota_watch_failed_total = %v after a hung tick, want 1", got)
	}
}

// quotaWatch runs watchQuotas over whatever sweep the test stages, logging to
// a buffer and recording on a Prometheus registry.
type quotaWatch struct {
	job      task
	log      bytes.Buffer
	recorder *metrics.Prometheus
	sweep    provisioning.QuotaSweep
	err      error
}

// The budget is 100 bytes, which the cluster tests measure against.
func newQuotaWatch() *quotaWatch {
	w := &quotaWatch{recorder: metrics.NewPrometheus()}
	w.job = watchQuotas(slog.New(slog.NewTextHandler(&w.log, nil)),
		func(context.Context) (provisioning.QuotaSweep, error) { return w.sweep, w.err },
		metrics.NewGameQuotaMetrics(w.recorder, 100), quotaWatchTimeout)
	return w
}

func TestWatchQuotasWarnsAndCountsOncePerTransition(t *testing.T) {
	w := newQuotaWatch()
	contest := uuid.New()
	w.sweep = provisioning.QuotaSweep{
		OverQuota: 2, Complete: true,
		Exceeded: []provisioning.QuotaTransition{{Database: "game_c1_u1", ContestID: contest, SizeBytes: 30 << 20, QuotaBytes: 20 << 20}},
		Restored: []provisioning.QuotaTransition{{Database: "game_c1_u2", ContestID: contest, SizeBytes: 6 << 20, QuotaBytes: 20 << 20}},
	}

	if err := w.job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}

	logged := w.log.String()
	if strings.Count(logged, "reached its disk quota") != 1 || !strings.Contains(logged, "game_c1_u1") ||
		!strings.Contains(logged, "size_bytes=31457280") || !strings.Contains(logged, "quota_bytes=20971520") {
		t.Fatalf("the copy over its quota was not warned about once, with its measurements: %q", logged)
	}
	if strings.Count(logged, "back within its disk quota") != 1 || !strings.Contains(logged, "game_c1_u2") {
		t.Fatalf("the copy back within its quota was not reported once: %q", logged)
	}
	// A crossing needs a look, a recovery does not.
	if levelOf(logged, "reached its disk quota") != "WARN" || levelOf(logged, "back within its disk quota") != "INFO" {
		t.Fatalf("the crossing must log at WARN and the recovery at INFO: %q", logged)
	}
	for name, want := range map[string]float64{
		"game_instances_over_quota":           2,
		"game_instances_quota_exceeded_total": 1,
		"game_instances_quota_restored_total": 1,
		"game_quota_watch_failed_total":       0,
	} {
		if got := seriesValues(t, w.recorder)[name]; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	// The next tick changed nothing: no new line, the counters stay.
	w.log.Reset()
	w.sweep.Exceeded, w.sweep.Restored = nil, nil
	if err := w.job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if w.log.Len() != 0 {
		t.Fatalf("an unchanged tick logged %q", w.log.String())
	}
	if got := seriesValues(t, w.recorder)["game_instances_quota_exceeded_total"]; got != 1 {
		t.Fatalf("game_instances_quota_exceeded_total = %v after an unchanged tick, want 1", got)
	}
}

// An incomplete tick counted only what it reached, so the gauge keeps the
// last complete count; what it did record is still counted.
func TestAFailedQuotaTickIsCountedAndKeepsTheLastCount(t *testing.T) {
	w := newQuotaWatch()
	w.sweep = provisioning.QuotaSweep{OverQuota: 3, Complete: true}
	if err := w.job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}

	failure := errors.New("the game cluster is away")
	w.sweep = provisioning.QuotaSweep{
		OverQuota: 1, Complete: false,
		Exceeded: []provisioning.QuotaTransition{{Database: "game_c1_u1"}},
	}
	w.err = failure
	if err := w.job.run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("run() = %v, want %v", err, failure)
	}

	values := seriesValues(t, w.recorder)
	if values["game_instances_over_quota"] != 3 {
		t.Errorf("game_instances_over_quota = %v after an incomplete tick, want the last complete 3", values["game_instances_over_quota"])
	}
	if values["game_quota_watch_failed_total"] != 1 || values["game_instances_quota_exceeded_total"] != 1 {
		t.Errorf("a failed tick counted %v failures and %v transitions, want 1 and 1",
			values["game_quota_watch_failed_total"], values["game_instances_quota_exceeded_total"])
	}
}

// levelOf is the level of the first line of logged that carries message.
func levelOf(logged, message string) string {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, message) {
			for _, field := range strings.Fields(line) {
				if level, ok := strings.CutPrefix(field, "level="); ok {
					return level
				}
			}
		}
	}
	return ""
}

func clusterWarnings(buf *bytes.Buffer) (into, out int) {
	return strings.Count(buf.String(), "the game cluster is near its disk budget"),
		strings.Count(buf.String(), "the game cluster is back below its disk warning")
}

// The cluster's size is a gauge every tick; the warning is a line when the
// threshold is crossed, either way, and silence in between.
func TestTheClusterWarningIsLoggedOnTheCrossingOnly(t *testing.T) {
	w := newQuotaWatch()
	tick := func(used int64) {
		t.Helper()
		w.sweep = provisioning.QuotaSweep{Complete: true,
			Cluster: provisioning.ClusterUsage{Bytes: used, Budget: 100, Known: true}}
		if err := w.job.run(t.Context()); err != nil {
			t.Fatalf("run() = %v", err)
		}
	}

	tick(50)
	if into, out := clusterWarnings(&w.log); into != 0 || out != 0 {
		t.Fatalf("a cluster at half its budget logged %q", w.log.String())
	}
	if got := seriesValues(t, w.recorder)["game_cluster_bytes"]; got != 50 {
		t.Fatalf("game_cluster_bytes = %v, want 50", got)
	}

	tick(85)
	tick(90)
	if into, _ := clusterWarnings(&w.log); into != 1 {
		t.Fatalf("two ticks past the threshold logged %d warnings, want 1: %q", into, w.log.String())
	}
	values := seriesValues(t, w.recorder)
	if values["game_cluster_bytes"] != 90 || values["game_cluster_max_bytes"] != 100 || values["game_cluster_near_budget"] != 1 {
		t.Fatalf("the cluster series came back %v", values)
	}

	tick(60)
	tick(55)
	if _, out := clusterWarnings(&w.log); out != 1 {
		t.Fatalf("two ticks back below logged %d lines, want 1: %q", out, w.log.String())
	}
	if levelOf(w.log.String(), "near its disk budget") != "WARN" || levelOf(w.log.String(), "back below its disk warning") != "INFO" {
		t.Fatalf("the crossing must log at WARN and the recovery at INFO: %q", w.log.String())
	}
	if got := seriesValues(t, w.recorder)["game_cluster_near_budget"]; got != 0 {
		t.Fatalf("game_cluster_near_budget = %v below the threshold, want 0", got)
	}

	// An unmeasured tick claims nothing either way and leaves the gauges.
	w.sweep = provisioning.QuotaSweep{Complete: true, Cluster: provisioning.ClusterUsage{Budget: 100}}
	if err := w.job.run(t.Context()); err != nil {
		t.Fatalf("run() = %v", err)
	}
	if into, out := clusterWarnings(&w.log); into != 1 || out != 1 {
		t.Fatalf("an unmeasured tick logged a crossing: %q", w.log.String())
	}
	if got := seriesValues(t, w.recorder)["game_cluster_bytes"]; got != 55 {
		t.Fatalf("game_cluster_bytes = %v after an unmeasured tick, want the last 55", got)
	}
}
