package app

import (
	"context"
	"log/slog"
	"time"
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
