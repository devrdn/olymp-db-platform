package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryLog is the journal of everything participants asked the game database.
//
// The one deliberate exception to the append-only rule (section 5, point 7):
// a row is written before its query runs and updated with the result after, so
// the application is allowed to update the outcome fields of its own row.
// audit_log stays strictly append-only.
type QueryLog struct{ pool *pgxpool.Pool }

var _ queryrunner.Journal = (*QueryLog)(nil)

// NewQueryLog returns a journal over pool.
func NewQueryLog(pool *pgxpool.Pool) *QueryLog { return &QueryLog{pool: pool} }

func (l *QueryLog) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, l.pool)
}

// Begin records a query that is about to run.
//
// The row lands at `running`, which is what makes a crash mid-query visible
// rather than silent: the alternative — writing one row afterwards — loses
// exactly the queries worth knowing about.
func (l *QueryLog) Begin(ctx context.Context, entry queryrunner.Entry) (int64, error) {
	var id int64
	err := l.querier(ctx).QueryRow(ctx, `
		INSERT INTO query_log (registration_id, request_id, sql_text, status)
		VALUES ($1, $2, $3, 'running')
		RETURNING id`,
		entry.Registration, entry.RequestID, entry.SQL).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("open a query log row: %w", err)
	}
	return id, nil
}

// Complete fills in how the query ended.
//
// Unconditional on the current status: a row the sweeper has already given up
// on may still be closed by a late but truthful result, and the truth is worth
// more than the guess. An update that matches nothing is an error rather than
// a silent no-op, because the only way it happens is a bug.
func (l *QueryLog) Complete(ctx context.Context, id int64, outcome queryrunner.Outcome) error {
	tag, err := l.querier(ctx).Exec(ctx, `
		UPDATE query_log
		SET status       = $2,
		    error_text   = nullif($3, ''),
		    duration_ms  = $4,
		    row_count    = $5,
		    completed_at = now()
		WHERE id = $1`,
		id, string(outcome.Status), outcome.Error,
		clamped(outcome.Duration.Milliseconds()), clamped(int64(outcome.Rows)))
	if err != nil {
		return fmt.Errorf("close query log row %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("close query log row %d: no such row", id)
	}
	return nil
}

// clamped fits a count into the column's int, without wrapping.
//
// Both values are bounded in practice — the runner's deadline is seconds and
// its row limit a thousand — but a plain conversion turns a number that got
// past those into a negative one, silently, in the journal that exists to be
// believed. A saturated value is wrong in a way anybody can see; a wrapped one
// is wrong in a way nobody can.
func clamped(value int64) int32 {
	switch {
	case value < 0:
		return 0
	case value > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(value)
	}
}

// SweepAbandoned marks rows whose process died before it could close them.
//
// A row stuck at `running` is indistinguishable from a query still in flight,
// so the cut-off has to be comfortably longer than any query can legitimately
// take — the Query Runner's deadline is five seconds. It reports how many it
// marked, which is a number worth alerting on: it counts crashes.
func (l *QueryLog) SweepAbandoned(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := l.querier(ctx).Exec(ctx, `
		UPDATE query_log
		SET status       = 'error',
		    error_text   = coalesce(error_text, 'abandoned: no result was ever recorded'),
		    completed_at = now()
		WHERE status = 'running'
		  AND executed_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("sweep abandoned query log rows: %w", err)
	}
	return tag.RowsAffected(), nil
}
