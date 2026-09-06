package app

import (
	"context"
	"log/slog"
	"time"

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
func tendPools(log *slog.Logger, service *provisioning.Service, depth int) task {
	return task{
		name:  "game-pool",
		every: 10 * time.Minute,
		run: func(ctx context.Context) error {
			made, dropped, err := service.Tend(ctx, func(provisioning.Contest) int { return depth })
			if made > 0 || dropped > 0 {
				log.InfoContext(ctx, "tended the game pools", "created", made, "dropped", dropped)
			}
			return err
		},
	}
}
