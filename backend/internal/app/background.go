package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// task is a periodic job. The machinery is kept minimal: no retries, backoff
// or leader election.
type task struct {
	name  string
	every time.Duration
	// atStart runs the job once as the process comes up. It is per job: a
	// job somebody is waiting on (the pool tender before a contest opens)
	// wants it; one bounded by a grace period of minutes to days does not.
	atStart bool
	// wake, when set, runs the job immediately on a send, besides its ticks.
	// nil for every job but the pool tender; a nil channel is never selected.
	wake <-chan struct{}
	run  func(context.Context) error
}

// runPeriodically runs the task until the context is cancelled. A failure,
// including at startup, is logged and the next tick still happens: these jobs
// are housekeeping, not conditions for running.
func runPeriodically(ctx context.Context, log *slog.Logger, t task) {
	ticker := time.NewTicker(t.every)
	defer ticker.Stop()

	// The ticker starts first, so a slow first run does not delay the second.
	if t.atStart {
		if ctx.Err() == nil {
			runOnce(ctx, log, t)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce(ctx, log, t)
		case <-t.wake:
			runOnce(ctx, log, t)
		}
	}
}

// runOnce is one run of a job, with its failure logged.
func runOnce(ctx context.Context, log *slog.Logger, t task) {
	if err := t.run(ctx); err != nil {
		log.ErrorContext(ctx, "a background job failed", "job", t.name, "error", err)
	}
}

// provisionStatementTimeout bounds one statement on the provisioning pool.
// Copying a template takes as long as its size requires; the bound only
// catches a hung cluster (CLAUDE.md rule 15).
const provisionStatementTimeout = 10 * time.Minute

// abandonedAfter is how long a query log row may sit at `running` before it is
// taken to belong to a dead process: far above the Query Runner's deadline
// plus queueing, so a slow query is never marked.
const abandonedAfter = 2 * time.Minute

// sweepQueryLog closes rows a process left at `running` when it died
// mid-query; the journal writes them before the query runs so a crash leaves
// evidence.
func sweepQueryLog(log *slog.Logger, sweep func(context.Context, time.Duration) (int64, error)) task {
	return task{
		name: "query-log-sweep",
		// At startup too: the process that died is often the one this
		// replaced. One UPDATE over a partial index.
		atStart: true,
		every:   time.Minute,
		run: func(ctx context.Context) error {
			swept, err := sweep(ctx, abandonedAfter)
			if err != nil {
				return err
			}
			if swept > 0 {
				log.WarnContext(ctx, "closed query log rows left open by a crash", "rows", swept)
			}
			return nil
		},
	}
}

// scheduleTickInterval bounds how late a contest's start or finish can be.
// Each tick competes afresh for the scheduler's advisory lock, so a dead
// replica costs only the tick it missed. A tick is two indexed UPDATEs and a
// pg_try_advisory_xact_lock.
const scheduleTickInterval = 15 * time.Second

// advanceContestSchedule runs one tick of contests.Scheduler.Advance.
func advanceContestSchedule(log *slog.Logger, advance func(context.Context) (int, int, error)) task {
	return task{
		name: "contest-schedule",
		// At startup too: participants' screens wait on it, and a tick is
		// cheap.
		atStart: true,
		every:   scheduleTickInterval,
		run: func(ctx context.Context) error {
			started, finished, err := advance(ctx)
			if err != nil {
				return err
			}
			if started > 0 || finished > 0 {
				log.InfoContext(ctx, "advanced the contest schedule", "started", started, "finished", finished)
			}
			return nil
		},
	}
}

// tendPools keeps every live contest's pool stocked and free of stale copies,
// so CREATE DATABASE happens here rather than in a participant's page load.
// A tick costs one query per live contest.
//
// tender, when given, runs it between ticks when a contest starts or a roster
// grows; triggers fire after their transaction commits and never block. A
// pool refused its depth (provisioning.Constrained) is logged with the
// measurements, which answer "why did the pool stop growing".
func tendPools(log *slog.Logger, service *provisioning.Service, limits provisioning.PoolLimits, tender *provisioning.Tender) task {
	var wake <-chan struct{}
	if tender != nil {
		wake = tender.C()
	}
	return task{
		name: "game-pool",
		// At startup: an untended pool after a restart means participants
		// waiting on CREATE DATABASE.
		atStart: true,
		every:   time.Minute,
		wake:    wake,
		run: func(ctx context.Context) error {
			depth := service.RosterDepth(limits, func(ctx context.Context, contest provisioning.Contest, sizing provisioning.Sizing) {
				// Warning: participants will wait, or the cluster is nearly
				// full.
				log.WarnContext(ctx, "a game pool was not allowed the depth its roster asked for",
					"contest", contest.ID, "bound", string(sizing.Bound),
					"granted", sizing.Depth, "wanted", sizing.Wanted,
					"template_bytes", sizing.TemplateBytes,
					"cluster_bytes", sizing.ClusterBytes, "budget_bytes", sizing.Budget)
			})
			made, dropped, err := service.Tend(ctx, depth)
			if made > 0 || dropped > 0 {
				log.InfoContext(ctx, "tended the game pools", "created", made, "dropped", dropped)
			}
			return err
		},
	}
}

// stuckOverdueBucket counts whole days past stuckAfter, so a stuck database
// is warned about again only when another full day has passed.
func stuckOverdueBucket(overdue time.Duration) int {
	return int(overdue / (24 * time.Hour))
}

// reclaimInstances drops every participant database whose contest finished
// longer ago than its grace period. reclaim is provisioning.Service.Reclaim,
// passed as a function so this wrapping is testable alone. Every ten minutes:
// a grace period is minutes at the shortest.
func reclaimInstances(log *slog.Logger, reclaim func(context.Context, int) (provisioning.ReclaimResult, error), graceMin int, counters *metrics.GameReclaimCounters) task {
	// stuckLogged holds the stuckOverdueBucket of each database at its last
	// warning, so a stuck database is logged once per change, not every tick.
	// Unlocked: runPeriodically never runs two ticks of a task at once.
	stuckLogged := make(map[string]int)

	return task{
		name: "game-reclaim",
		// Not at startup: nothing waits on it, and a boot-time DROP DATABASE
		// storm would compete with tendPools for the provisioning pool during
		// a restart, repeated on every boot of a crash loop.
		every: 10 * time.Minute,
		run: func(ctx context.Context) error {
			result, err := reclaim(ctx, graceMin)
			counters.AddInstances(result.Reclaimed, result.Skipped, result.Failed)
			counters.AddTemplates(result.TemplatesReclaimed, result.TemplatesFailed)
			if result.Reclaimed > 0 || result.Skipped > 0 || result.Failed > 0 ||
				result.TemplatesReclaimed > 0 || result.TemplatesFailed > 0 {
				// Logged even on success; skipped is included so "nothing to
				// do" and "everything busy" read differently.
				log.InfoContext(ctx, "reclaimed game instances",
					"reclaimed", result.Reclaimed, "skipped", result.Skipped, "failed", result.Failed,
					"templates_reclaimed", result.TemplatesReclaimed, "templates_failed", result.TemplatesFailed)
			}
			// A database stuck long past its grace gets its own warning,
			// once per change (see stuckLogged).
			seen := make(map[string]bool, len(result.Stuck))
			for _, s := range result.Stuck {
				seen[s.Database] = true
				bucket := stuckOverdueBucket(s.Overdue)
				if last, warned := stuckLogged[s.Database]; !warned || bucket != last {
					log.WarnContext(ctx, "a game instance has been busy long past its grace deadline",
						"database", s.Database, "contest", s.ContestID, "overdue", s.Overdue.Round(time.Minute).String())
					stuckLogged[s.Database] = bucket
				}
			}
			// Anything no longer stuck is forgotten, so getting stuck again
			// warns again.
			for database := range stuckLogged {
				if !seen[database] {
					delete(stuckLogged, database)
				}
			}
			return err
		},
	}
}

// buildGamesEvery is short because an organiser is watching the build
// status; an idle tick is one indexed row read.
const buildGamesEvery = 5 * time.Second

// staleBuildMargin is the room the stale cut-off leaves beyond the script's
// own budget: GAME_BUILD_TIMEOUT covers only the script, and the surrounding
// DROP/CREATE DATABASE and grants are bounded by provisionStatementTimeout.
const staleBuildMargin = provisionStatementTimeout

// staleBuildAfter is how long a game may sit in `building` before another tick
// takes it back. It must outlast any build still alive, because a second
// BuildTemplate drops the template the first is filling. So it is derived
// from the deployment's build timeout (CLAUDE.md rule 11) rather than a
// constant; a crashed build recovers slower, which is recoverable, while two
// racing builds are not.
func staleBuildAfter(buildTimeout time.Duration) time.Duration {
	return buildTimeout + staleBuildMargin
}

// buildGames builds one waiting game per tick, outside any HTTP request, and
// recovers builds a dead process left in `building`. buildTimeout is the
// value the provisioner was given, so one number decides both how long a
// build may run and when it is presumed dead.
func buildGames(
	log *slog.Logger,
	build func(context.Context, time.Duration) (provisioning.Template, error),
	buildTimeout time.Duration,
) task {
	stale := staleBuildAfter(buildTimeout)
	return task{
		name: "game-build",
		// At startup too: a boot is when a stale build is most likely.
		atStart: true,
		every:   buildGamesEvery,
		run: func(ctx context.Context) error {
			built, err := build(ctx, stale)
			if errors.Is(err, provisioning.ErrNoGame) {
				return nil
			}
			if built.Status == provisioning.TemplateFailed {
				// `error` is what the organiser saw; for an internal failure
				// the real cause is err below, logged only here.
				log.WarnContext(ctx, "a game failed to build",
					"contest", built.ContestID, "version", built.Version, "error", built.BuildError)
			}
			if err != nil {
				return err
			}
			if built.Status == provisioning.TemplateFailed {
				return nil
			}
			log.InfoContext(ctx, "built a game",
				"contest", built.ContestID, "version", built.Version, "database", built.Database)
			return nil
		},
	}
}

// abandonedUploadsEvery is how often the janitor looks for abandoned uploads
// and unnamed files; nothing here is urgent.
const abandonedUploadsEvery = 10 * time.Minute

// abandonedUploads aborts 'receiving' uploads idle past olderThan and
// removes files no row names (provisioning.Games.SweepUploads).
func abandonedUploads(log *slog.Logger, games *provisioning.Games, olderThan time.Duration) task {
	return task{
		name: "game-upload-sweep",
		// Not at startup: nothing is urgent, and a directory walk would
		// compete with the restart.
		every: abandonedUploadsEvery,
		run: func(ctx context.Context) error {
			result, err := games.SweepUploads(ctx, olderThan)
			if result.Abandoned > 0 || result.OrphanFiles > 0 {
				log.InfoContext(ctx, "swept abandoned uploads",
					"abandoned", result.Abandoned, "orphan_files", result.OrphanFiles)
			}
			return err
		},
	}
}
