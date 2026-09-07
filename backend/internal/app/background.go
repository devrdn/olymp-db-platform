package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// A periodic job, and the small amount of machinery the service needs to run
// one. Deliberately small: there is one job today, and a scheduler with
// retries, backoff and leader election would be more moving parts than the
// thing it schedules.
type task struct {
	name  string
	every time.Duration
	run   func(context.Context) error
}

// runPeriodically runs the task until the context is cancelled.
//
// A failure is logged and the next tick still happens. These jobs are
// housekeeping: one that cannot run because the database is briefly away
// should try again in a minute, not take the service down with it.
func runPeriodically(ctx context.Context, log *slog.Logger, t task) {
	ticker := time.NewTicker(t.every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.run(ctx); err != nil {
				log.ErrorContext(ctx, "a background job failed", "job", t.name, "error", err)
			}
		}
	}
}

// provisionStatementTimeout bounds one statement on the game cluster's
// provisioning pool. Minutes rather than the core API's seconds, because
// copying a template is disk work whose duration is the template's size; the
// bound exists so a hung cluster is still noticed, not to shape normal work.
const provisionStatementTimeout = 10 * time.Minute

// abandonedAfter is how long a query log row may sit at `running` before it is
// taken to belong to a process that died.
//
// Far above anything a live query can reach: the Query Runner's own deadline
// is five seconds, and admission control bounds the wait in front of it. The
// margin is what stops the sweeper marking a query that is merely slow.
const abandonedAfter = 2 * time.Minute

// sweepQueryLog closes rows abandoned by a process that died mid-query.
//
// The other half of the two-phase journal: rows are written before the query
// runs precisely so a crash leaves evidence, and evidence nobody ever closes
// is a row that says `running` for ever. Without this job the guarantee is
// only half-built, which is worse than not claiming it.
func sweepQueryLog(log *slog.Logger, sweep func(context.Context, time.Duration) (int64, error)) task {
	return task{
		name:  "query-log-sweep",
		every: time.Minute,
		run: func(ctx context.Context) error {
			swept, err := sweep(ctx, abandonedAfter)
			if err != nil {
				return err
			}
			if swept > 0 {
				// Worth a line at warning level: this number counts processes
				// that died in the middle of a participant's query.
				log.WarnContext(ctx, "closed query log rows left open by a crash", "rows", swept)
			}
			return nil
		},
	}
}

// scheduleTickInterval bounds how long a dead replica can delay a contest's
// published → running or running → finished transition (§8): each tick is a
// fresh competition for contests.Scheduler's advisory lock, never held
// between ticks, so a dead replica costs the next tick nothing but the one it
// missed. Short because this gates what a participant's own screen and
// events channel report the moment a contest's window opens or closes, not
// because the lock itself is expensive to ask for — two indexed UPDATEs and
// one pg_try_advisory_xact_lock, whether or not either UPDATE matches a row.
const scheduleTickInterval = 15 * time.Second

// advanceContestSchedule runs one tick of the background scheduler
// (contests.Scheduler.Advance): move every contest whose window opened or
// closed, or do nothing this tick because another replica already has it.
func advanceContestSchedule(log *slog.Logger, advance func(context.Context) (int, int, error)) task {
	return task{
		name:  "contest-schedule",
		every: scheduleTickInterval,
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

// tendPools keeps every live contest's pool stocked and free of stale copies.
//
// The background half of section 4.2, and the reason a participant arriving
// mid-contest does not wait: CREATE DATABASE happens here, on a timer, while
// nothing depends on it. Every ten minutes rather than every minute — a pool
// drains at the speed people register, which is not a per-minute event, and
// each tick may create databases.
// tendPools keeps every live contest's pool as deep as its own roster asks
// for — see provisioning.Service.RosterDepth, and the flat depth it replaces.
func tendPools(log *slog.Logger, service *provisioning.Service, headroom, max int) task {
	return task{
		name:  "game-pool",
		every: 10 * time.Minute,
		run: func(ctx context.Context) error {
			made, dropped, err := service.Tend(ctx, service.RosterDepth(headroom, max))
			if made > 0 || dropped > 0 {
				log.InfoContext(ctx, "tended the game pools", "created", made, "dropped", dropped)
			}
			return err
		},
	}
}

// stuckOverdueBucket turns a raw Overdue duration into the granularity the
// de-duplication below actually cares about: whole days past stuckAfter.
// Overdue grows every tick a database stays stuck, so comparing the raw
// duration between ticks would never see two ticks agree — this is what lets
// "still stuck, ten minutes more overdue than last tick" read as unchanged
// while "still stuck, a full day more overdue" reads as a change worth a
// fresh line.
func stuckOverdueBucket(overdue time.Duration) int {
	return int(overdue / (24 * time.Hour))
}

// reclaimInstances drops every participant database whose contest finished
// longer ago than its grace period, and marks its row 'dropped' — the
// background half of §2.4. reclaim is provisioning.Service.Reclaim, taken as
// a function so the wrapping here (the log line, the metrics pair) can be
// proven without standing up a real Service; Reclaim's own rules — the grace
// period, leaving a busy database for the next tick, one failure not
// stopping the rest — are exercised where they are declared
// (internal/provisioning/reclaim_test.go and, for the busy-database refusal
// against a real cluster, internal/gamedb/provisioner_test.go).
//
// The same ten-minute cadence as tendPools: a grace period is configured in
// minutes at the shortest, so nothing meaningful is lost by checking on the
// same schedule the pool is already tended on rather than a faster one.
func reclaimInstances(log *slog.Logger, reclaim func(context.Context, int) (provisioning.ReclaimResult, error), graceMin int, counters *metrics.GameReclaimCounters) task {
	// stuckLogged remembers, for every database this process has already
	// warned about, the stuckOverdueBucket it was in at the last warning —
	// what lets the run closure below log a database once when it becomes
	// stuck and again only when that fact actually changes, rather than once
	// every ten minutes for as long as it stays busy (144 lines a day per
	// database otherwise: visible stops being useful and becomes noise an
	// operator learns to ignore, which is the same as silence). Safe
	// unlocked: runPeriodically (this file) never runs two ticks of the same
	// task concurrently, so nothing else ever touches this map.
	stuckLogged := make(map[string]int)

	return task{
		name:  "game-reclaim",
		every: 10 * time.Minute,
		run: func(ctx context.Context) error {
			result, err := reclaim(ctx, graceMin)
			counters.AddInstances(result.Reclaimed, result.Skipped, result.Failed)
			counters.AddTemplates(result.TemplatesReclaimed, result.TemplatesFailed)
			if result.Reclaimed > 0 || result.Skipped > 0 || result.Failed > 0 ||
				result.TemplatesReclaimed > 0 || result.TemplatesFailed > 0 {
				// Worth a line even on plain success: this is the number an
				// organizer who cannot find a database has no other way to
				// notice moved at all, short of reading the audit trail one
				// contest at a time. Skipped is included here on purpose —
				// "reclaimed=0 failed=0" used to read as "nothing to do" even
				// when every candidate was left busy for the next tick.
				log.InfoContext(ctx, "reclaimed game instances",
					"reclaimed", result.Reclaimed, "skipped", result.Skipped, "failed", result.Failed,
					"templates_reclaimed", result.TemplatesReclaimed, "templates_failed", result.TemplatesFailed)
			}
			// Named separately from the summary line above rather than
			// folded into it: a database stuck this long past its grace is
			// not "one more of the routine skips a busy tick always has", it
			// is the specific thing an operator should go look at. But it is
			// named once per fact, not once per tick — see stuckLogged above
			// for why a repeat with nothing changed is silent.
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
			// Anything no longer stuck — reclaimed, or simply back under
			// stuckAfter — starts clean. If it becomes stuck again later that
			// is a new fact, worth its own first warning, not silence because
			// this process once warned about the same database before.
			for database := range stuckLogged {
				if !seen[database] {
					delete(stuckLogged, database)
				}
			}
			return err
		},
	}
}

// buildGamesEvery is how often the builder looks for a game waiting to be
// built.
//
// Short, unlike every other job in this file, because on the other end of it
// is an organiser who has just pressed save and is watching a status. Each
// tick that finds nothing is one indexed row read; a tick that finds
// something does minutes of work and the next one simply finds nothing while
// it runs.
const buildGamesEvery = 5 * time.Second

// staleBuildAfter is how long a game may sit in `building` before another
// tick takes it back.
//
// This is not "how long a build may take" — a build that is still running
// holds nothing that stops a second one starting beside it, so the figure has
// to be comfortably longer than any real build rather than a timeout on one.
// Fifteen minutes is far past the seconds an olympiad's game actually takes
// and far short of leaving somebody watching a dead spinner for an afternoon.
const staleBuildAfter = 15 * time.Minute

// buildGames builds one waiting game per tick.
//
// The job exists because building creates a database and runs an author's
// whole script inside it, which is not something to hold an HTTP request open
// for — and because an API that dies mid-build has to leave the work
// recoverable rather than a row stuck in `building` for ever.
func buildGames(log *slog.Logger, games *provisioning.Games) task {
	return task{
		name:  "game-build",
		every: buildGamesEvery,
		run: func(ctx context.Context) error {
			built, err := games.Build(ctx, staleBuildAfter)
			if errors.Is(err, provisioning.ErrNoGame) {
				// Nothing waiting, which is what almost every tick finds.
				return nil
			}
			if err != nil {
				return err
			}
			if built.Status == provisioning.TemplateFailed {
				// The organiser sees this on their own screen; the line is
				// here so an operator reading the log knows why a contest
				// cannot be published.
				log.WarnContext(ctx, "a game failed to build",
					"contest", built.ContestID, "version", built.Version, "error", built.BuildError)
				return nil
			}
			log.InfoContext(ctx, "built a game",
				"contest", built.ContestID, "version", built.Version, "database", built.Database)
			return nil
		},
	}
}
