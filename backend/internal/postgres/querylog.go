package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryLog is the journal of everything participants asked the game database.
//
// The one deliberate exception to the append-only rule (section 5, point 7):
// a row is written before its query runs and updated with the result after, so
// the application is allowed to update the outcome fields of its own row.
// audit_log stays strictly append-only.
//
// # A row this journal never sees
//
// Everything that reaches Begin does so under a request that a policy, a
// rate, or the database itself may still refuse — that is the whole of
// section 5, point 7's "journal everything, including what validation
// rejects". One refusal is not in that "everything": queryproxy.Service's own
// pre-check refuses a participant asking too fast, or one whose SQL is over
// the length bound, *before* Begin is ever called — that is the entire point
// of that check (see queryproxy.Service.Run's doc), since the alternative is
// a journal row, and the GIN index it costs, for a query that never touched
// the database. So the panel described in docs/ARCHITECTURE.md §9.1, and the
// per-participant counts reporting builds over this table, undercount rate
// refusals by however many this façade caught first — accepted rather than
// worked around, because working around it means paying the row the fix was
// written to avoid. What still counts a rate refusal, at whatever coarseness
// the panel does not offer, is the ordinary per-route HTTP metric
// (platform/metrics.Recorder.ObserveRequest) on this endpoint's 429s — no new
// metric was added for this, because that one already answers "how often".
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

// History returns one page of registrationID's own rows, newest first, and
// how many rows match in total — the participant's own query log, and only
// that participant's: the WHERE clause below is exactly this registration_id
// and nothing a caller otherwise controls, the same guarantee Access itself
// gives the story and the questions endpoints.
//
// Backed by query_log_registration_executed_idx (migration 000004), already
// built for exactly this — its own comment names both this and the admin
// journal panel that will one day share it — so this endpoint needs no
// migration of its own (CLAUDE.md rule 7).
func (l *QueryLog) History(ctx context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error) {
	limit, offset = queryrunner.NormalizeHistoryPage(limit, offset)

	rows, err := l.querier(ctx).Query(ctx, `
		SELECT sql_text, status, COALESCE(error_text, ''), duration_ms, row_count, executed_at,
		       COUNT(*) OVER() AS total
		FROM query_log
		WHERE registration_id = $1
		ORDER BY executed_at DESC, id DESC
		LIMIT $2 OFFSET $3`,
		registrationID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("read query history for registration %s: %w", registrationID, err)
	}
	defer rows.Close()

	var (
		found []queryrunner.HistoryEntry
		total int
	)
	for rows.Next() {
		var (
			entry  queryrunner.HistoryEntry
			status string
		)
		if err := rows.Scan(&entry.SQL, &status, &entry.Error, &entry.DurationMs, &entry.RowCount,
			&entry.ExecutedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan query history row: %w", err)
		}
		entry.Status = queryrunner.Status(status)
		found = append(found, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read query history for registration %s: %w", registrationID, err)
	}
	return found, total, nil
}

// ExportHistory streams every one of registrationID's own rows to yield,
// oldest first — the read behind the participant's CSV download of their own
// query log (§9.1: CSV streams row by row with no volume ceiling).
//
// Streamed rather than paged, and that is the difference from History. A page
// exists because a screen shows one; a file is the whole record, and a
// participant who ran nine hundred queries over a two-hour olympiad would
// otherwise get a file quietly missing eight hundred of them. Nothing here is
// held in memory beyond the row being written: pgx hands rows over one at a
// time and yield writes each straight to the socket, so the memory this costs
// is one row rather than one contest's worth of them.
//
// Oldest first, unlike History's newest-first page. The file is a record of a
// session and is read top to bottom, the way the session happened; the panel
// is a lookup and answers "what did I just run". Both orderings are served by
// query_log_registration_executed_idx (migration 000004) — an index scan runs
// either direction — so this needs no migration of its own (CLAUDE.md rule 7).
//
// The same WHERE clause History has, and the same guarantee: exactly this
// registration_id, nothing a caller otherwise controls.
//
// A yield that returns an error stops the stream and is returned as it is.
// The caller is writing to a socket, and a client that hung up must not have
// the rest of the log read out of the database on its behalf.
func (l *QueryLog) ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) error {
	rows, err := l.querier(ctx).Query(ctx, `
		SELECT sql_text, status, COALESCE(error_text, ''), duration_ms, row_count, executed_at
		FROM query_log
		WHERE registration_id = $1
		ORDER BY executed_at ASC, id ASC`,
		registrationID)
	if err != nil {
		return fmt.Errorf("read the query log of registration %s: %w", registrationID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			entry  queryrunner.HistoryEntry
			status string
		)
		if err := rows.Scan(&entry.SQL, &status, &entry.Error, &entry.DurationMs, &entry.RowCount,
			&entry.ExecutedAt); err != nil {
			return fmt.Errorf("scan a query log row: %w", err)
		}
		entry.Status = queryrunner.Status(status)
		if err := yield(entry); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the query log of registration %s: %w", registrationID, err)
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
