package postgres

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryLog is the journal of everything participants asked the game database.
//
// It is the one exception to the append-only rule (docs/ARCHITECTURE.md
// section 5, point 7): a row is written before its query runs and its outcome
// fields are updated after. audit_log stays strictly append-only.
//
// Requests that queryproxy.Service refuses before Begin (too fast, or SQL over
// the length bound) get no row, to avoid paying a row and its GIN index for a
// query that never reached the database. The query-log panel (section 9.1)
// and counts built over this table therefore undercount rate refusals; the
// per-route HTTP metric on this endpoint's 429s counts them.
type QueryLog struct{ pool *pgxpool.Pool }

var _ queryrunner.Journal = (*QueryLog)(nil)

// NewQueryLog returns a journal over pool.
func NewQueryLog(pool *pgxpool.Pool) *QueryLog { return &QueryLog{pool: pool} }

func (l *QueryLog) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, l.pool)
}

// Begin records a query that is about to run. The row starts at `running` so
// that a crash mid-query stays visible.
//
// The same insert stores the client address (NULL when unknown) and the
// monitor.ComparableFingerprint, so watching a participant needs no second
// write.
func (l *QueryLog) Begin(ctx context.Context, entry queryrunner.Entry) (int64, error) {
	var address *netip.Addr
	if entry.Address.IsValid() {
		address = &entry.Address
	}
	var id int64
	err := l.querier(ctx).QueryRow(ctx, `
		INSERT INTO query_log (registration_id, request_id, sql_text, status, ip, sql_fingerprint)
		VALUES ($1, $2, $3, 'running', $4, $5)
		RETURNING id`,
		entry.Registration, entry.RequestID, entry.SQL, address, monitor.ComparableFingerprint(entry.SQL)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("open a query log row: %w", err)
	}
	return id, nil
}

// Complete fills in how the query ended. It ignores the current status, so a
// late real result overwrites the sweeper's guess. Matching no row is an
// error: it only happens through a bug.
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
// the total count. The WHERE clause is only this registration_id, so a caller
// cannot read another participant's log. The ORDER BY reads
// query_log_registration_executed_idx backwards (CLAUDE.md rule 7).
//
// The total comes from registration_activity.queries, kept by the journal's
// insert trigger, instead of COUNT(*) OVER() on the page: the window function
// read every row of the registration before the LIMIT, a cost that grew with
// the whole history. Only the registration's cascade deletes journal rows, and
// it takes the counter too, so the two agree. The two reads are separate
// snapshots, so `total` can be one ahead of the page; it only decides whether
// to offer "load more".
//
// Statements are cut to queryrunner.MaxHistorySQLChars in the SELECT, so the
// bytes never reach the driver (CLAUDE.md rule 12). ExportHistory is not cut.
func (l *QueryLog) History(ctx context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error) {
	limit, offset = queryrunner.NormalizeHistoryPage(limit, offset)
	querier := l.querier(ctx)

	// The counter includes running queries. No row means no query yet.
	var total int
	if err := querier.QueryRow(ctx, `
		SELECT COALESCE((SELECT queries FROM registration_activity WHERE registration_id = $1), 0)`,
		registrationID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the query history of registration %s: %w", registrationID, err)
	}

	rows, err := querier.Query(ctx, `
		SELECT left(sql_text, $4), char_length(sql_text) > $4,
		       status, COALESCE(error_text, ''), duration_ms, row_count, executed_at
		FROM query_log
		WHERE registration_id = $1
		ORDER BY executed_at DESC, id DESC
		LIMIT $2 OFFSET $3`,
		registrationID, limit, offset, queryrunner.MaxHistorySQLChars)
	if err != nil {
		return nil, 0, fmt.Errorf("read query history for registration %s: %w", registrationID, err)
	}
	defer rows.Close()

	var found []queryrunner.HistoryEntry
	for rows.Next() {
		var (
			entry  queryrunner.HistoryEntry
			status string
		)
		if err := rows.Scan(&entry.SQL, &entry.SQLTruncated, &status, &entry.Error,
			&entry.DurationMs, &entry.RowCount, &entry.ExecutedAt); err != nil {
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

// exportCursor is the cursor ExportHistory reads from, and exportFetch is how
// many rows one FETCH takes.
//
// The name is interpolated into SQL, which is safe only because it is a
// constant. Fifty rows balances round trips against what PostgreSQL
// materialises per FETCH: at most 550 KiB even at sqlpolicy.MaxQueryBytes.
const (
	exportCursor = "query_log_export"
	exportFetch  = 50
)

// ExportHistory streams all of registrationID's own rows to yield, oldest
// first, for the participant's CSV download. Unlike History it is not paged:
// the file is the whole record. The WHERE clause is only this
// registration_id.
//
// The read is a cursor so that PostgreSQL streams too. A plain SELECT is
// planned for the last row and can choose a full Sort over a Bitmap Heap Scan,
// holding the whole log in memory; a cursor is planned for the first row
// (cursor_tuple_fraction) and reads query_log_registration_executed_idx
// (registration_id, executed_at, id) in order with no sort node (CLAUDE.md
// rule 7). No id tiebreak is needed: executed_at is transaction start, and a
// participant's queries run in separate serial transactions. PostgreSQL holds
// one FETCH and this process holds one row.
//
// A cursor needs a transaction; without one from the caller, the method opens
// its own. A yield error stops the stream and is returned unwrapped, so a
// client that hung up does not have the rest of the log read for it.
//
// Rows and bytes are bounded separately (CLAUDE.md rule 2):
// queryrunner.MaxExportRows as a LIMIT, queryrunner.MaxExportBytes counted as
// rows arrive. truncated reports that a bound stopped the stream. The caller's
// deadline on ctx bounds how long the connection is held.
func (l *QueryLog) ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (truncated bool, err error) {
	querier := l.querier(ctx)
	if !storage.InTx(ctx) {
		tx, err := l.pool.Begin(ctx)
		if err != nil {
			return false, fmt.Errorf("read the query log of registration %s: %w", registrationID, err)
		}
		// Nothing here writes, so rolling back is how this ends either way.
		defer func() { _ = tx.Rollback(ctx) }()
		querier = tx
	}

	// One row past the bound, so a log of exactly MaxExportRows is not
	// reported as truncated. The cursor is lazy, so exportBudget is the
	// operative bound; the LIMIT keeps the read bounded if it ever becomes a
	// plain SELECT.
	if _, err := querier.Exec(ctx, `
		DECLARE `+exportCursor+` NO SCROLL CURSOR FOR
		SELECT sql_text, status, COALESCE(error_text, ''), duration_ms, row_count, executed_at
		FROM query_log
		WHERE registration_id = $1
		ORDER BY executed_at ASC
		LIMIT $2`,
		registrationID, queryrunner.MaxExportRows+1); err != nil {
		return false, fmt.Errorf("read the query log of registration %s: %w", registrationID, err)
	}
	// Closed so a second export in the same transaction can reuse the name.
	defer func() { _, _ = querier.Exec(ctx, `CLOSE `+exportCursor) }()

	budget := exportBudget{rows: queryrunner.MaxExportRows, bytes: queryrunner.MaxExportBytes}
	fetch := fmt.Sprintf(`FETCH FORWARD %d FROM %s`, exportFetch, exportCursor)
	for {
		read, err := streamExportBatch(ctx, querier, fetch, &budget, yield)
		if err != nil {
			return budget.spent, err
		}
		if budget.spent {
			return true, nil
		}
		// A short batch is the end of the cursor.
		if read < exportFetch {
			return false, nil
		}
	}
}

// exportBudget is what one download may still take, in rows and in bytes of
// SQL. spent records that either bound ran out.
type exportBudget struct {
	rows  int
	bytes int
	spent bool
}

// take draws one statement against both bounds and reports whether it may be
// handed over. A row that does not fit is refused, not trimmed.
func (b *exportBudget) take(sql string) bool {
	if b.rows <= 0 || len(sql) > b.bytes {
		b.spent = true
		return false
	}
	b.rows--
	b.bytes -= len(sql)
	return true
}

// streamExportBatch hands one FETCH's rows to yield and returns how many it
// read. It is a separate function so the rows are closed on every path.
func streamExportBatch(ctx context.Context, querier storage.Querier, fetch string,
	budget *exportBudget, yield func(queryrunner.HistoryEntry) error) (int, error) {
	rows, err := querier.Query(ctx, fetch)
	if err != nil {
		return 0, fmt.Errorf("read a page of the query log: %w", err)
	}
	defer rows.Close()

	read := 0
	for rows.Next() {
		var (
			entry  queryrunner.HistoryEntry
			status string
		)
		if err := rows.Scan(&entry.SQL, &status, &entry.Error, &entry.DurationMs, &entry.RowCount,
			&entry.ExecutedAt); err != nil {
			return read, fmt.Errorf("scan a query log row: %w", err)
		}
		read++
		if !budget.take(entry.SQL) {
			return read, nil
		}
		entry.Status = queryrunner.Status(status)
		if err := yield(entry); err != nil {
			// Unwrapped: the caller recognises its own error.
			return read, err
		}
	}
	if err := rows.Err(); err != nil {
		return read, fmt.Errorf("read a page of the query log: %w", err)
	}
	return read, nil
}

// clamped fits a count into the column's int32 by saturating instead of
// wrapping, so an out-of-range value never shows up as a negative one.
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

// SweepAbandoned marks rows whose process died before closing them. A
// `running` row looks like a query in flight, so olderThan must be well above
// the Query Runner's five-second deadline. The count it returns counts crashes
// and is worth alerting on.
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
