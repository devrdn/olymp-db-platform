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
//
// The same insert records where the query came from and its fingerprint
// (monitor.Fingerprint), so watching a participant costs the console no
// second write. An address that could not be worked out is stored as NULL.
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
		entry.Registration, entry.RequestID, entry.SQL, address, monitor.Fingerprint(entry.SQL)).Scan(&id)
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
//
// # Two statements, and why the count is not part of the page
//
// The count used to be a `COUNT(*) OVER()` alongside the columns, which puts
// the WindowAgg *below* the Limit: measured on the core database with one
// registration holding nine hundred rows, reading fifty of them touched 909
// buffers and 1.28 ms, because the window has to see every row of that
// registration — heap included, wide sql_text column and all — before the
// limit may throw all but fifty away. At twenty thousand rows in one
// registration it also spilled the tuplestore to disk: 1033 shared buffers
// plus 1201 temp blocks written and read back, 12.55 ms. The cost grew with
// everything the participant had ever run, and the panel re-reads this on
// every visit to the tab.
//
// Split in two, the page is bounded by the page — `Limit -> Incremental Sort
// -> Index Scan`, 55 buffers and 0.12 ms for the same fifty rows — and the
// count is a narrow aggregate the same index answers on its own: an
// Index Only Scan, 11 buffers, no heap fetches, 0.13 ms. Two round trips
// instead of one, for an order of magnitude less work; and they are not read
// as one snapshot, so a row written between them makes `total` one ahead of
// the page. That is the same staleness any second request already had, on a
// number whose only job is to decide whether to offer "load more".
//
// # And why the statement is cut here
//
// See queryrunner.MaxHistorySQLChars: the row count was bounded and the bytes
// were not. The cut is in the SELECT so the bytes never reach the driver at
// all (CLAUDE.md rule 12) — `left` gives the beginning of the statement,
// `char_length` says whether there was more, and the flag travels with the
// row so nothing downstream has to guess. ExportHistory below is deliberately
// not cut: it is the record, and it streams.
func (l *QueryLog) History(ctx context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error) {
	limit, offset = queryrunner.NormalizeHistoryPage(limit, offset)
	querier := l.querier(ctx)

	var total int
	if err := querier.QueryRow(ctx,
		`SELECT count(*) FROM query_log WHERE registration_id = $1`,
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

// exportCursor is the cursor ExportHistory reads its rows off, and
// exportFetch is how many rows one FETCH takes from it.
//
// The name is a constant rather than anything a caller supplies: it is
// interpolated into SQL below, which is only safe because nothing outside
// this file can choose it. The cursor is closed before ExportHistory returns,
// so two exports in the same transaction do not collide over the name.
//
// Fifty is a compromise between two costs neither of which should dominate: a
// FETCH is a round trip, so one row at a time would be a round trip per row
// (3,600 of them for a participant who spent a whole olympiad at the rate
// limit), and a large batch is that many rows PostgreSQL materialises before
// the first one moves. Fifty rows is 550 KiB even at sqlpolicy.MaxQueryBytes,
// the ceiling no real statement is anywhere near, and one round trip per
// fifty rows.
const (
	exportCursor = "query_log_export"
	exportFetch  = 50
)

// ExportHistory streams every one of registrationID's own rows to yield,
// oldest first — the read behind the participant's CSV download of their own
// query log (§9.1: CSV streams row by row with no volume ceiling).
//
// Streamed rather than paged, and that is the difference from History. A page
// exists because a screen shows one; a file is the whole record, and a
// participant who ran nine hundred queries over a two-hour olympiad would
// otherwise get a file quietly missing eight hundred of them.
//
// # Why a cursor, and why the ordering has no id in it
//
// "Streams" has to be true of PostgreSQL as well as of this process, and it
// was true of neither the ordering nor the read this used to use.
//
// The ordering was `executed_at ASC, id ASC`, and the comment here claimed
// query_log_registration_executed_idx served it because "an index scan runs
// either direction". That index is (registration_id, executed_at DESC): it
// has no id in it, so the tiebreak is a sort the planner has to add on top —
// measured on the core database with 915 rows for one registration in a
// 61k-row journal, `Incremental Sort -> Index Scan Backward`, and at 3,615
// rows in a 120k-row journal a full `Sort` over a `Bitmap Heap Scan`, 1.8 MB
// of quicksort memory holding the participant's whole log — sql_text and all,
// a column bounded only by sqlpolicy.MaxQueryBytes at 64 KiB a row — before
// the first row could reach the socket.
//
// The id is gone rather than added to the index. Adding it was measured too
// and changes nothing: with (registration_id, executed_at, id) in place the
// planner still chose `Sort -> Bitmap Heap Scan` at the same 3,615 rows,
// because what it is avoiding there is 3,615 random heap fetches, not a
// missing ordering. So that index would have cost every participant's query
// a third index write — this table takes two writes per query, Begin and
// Complete — and bought nothing. What the tiebreak decided was the relative
// order of rows sharing an executed_at to the microsecond; executed_at
// defaults to now(), which is transaction start, and a participant's
// statements are serial requests in separate transactions, so that is a tie
// that does not arise. If it ever did, the two statements happened in the
// same microsecond and a record of a session has nothing to say about which
// came first.
//
// Dropping the tiebreak is necessary and not sufficient: with it gone the
// planner still preferred `Sort -> Bitmap Heap Scan` for a plain SELECT at
// 3,615 rows, because a plain SELECT is priced on the cost of the *last* row.
// A cursor is priced on the first (cursor_tuple_fraction, 0.1), which is the
// truth about this read — yield writes each row to a socket as it arrives.
// Measured on the same data, `EXPLAIN DECLARE ... CURSOR FOR` the query
// below is a bare `Index Scan Backward using
// query_log_registration_executed_idx`, no sort node at all; with the id
// tiebreak still in it, the same cursor plans `Incremental Sort` on top. So:
// no id, and a cursor. Still no migration of its own (CLAUDE.md rule 7) —
// the index that serves it is migration 000004's, unchanged.
//
// Memory is bounded on both sides. PostgreSQL holds one FETCH, this process
// holds one row: pgx hands the rows of a batch over one at a time and yield
// writes each straight to the socket.
//
// The transaction is this method's own when the caller has none, which is the
// arrangement the deployment uses — queryLogCSV calls this straight off the
// request context. A cursor needs a transaction, and the read this replaces
// already pinned a pool connection and held a snapshot for the whole download
// (an unfinished portal is inside an implicit transaction), so what is new
// here is the BEGIN, not the duration.
//
// The same WHERE clause History has, and the same guarantee: exactly this
// registration_id, nothing a caller otherwise controls.
//
// A yield that returns an error stops the stream and is returned as it is.
// The caller is writing to a socket, and a client that hung up must not have
// the rest of the log read out of the database on its behalf.
//
// # What bounds it
//
// Streaming is not the same as unbounded, and this read used to be both
// (CLAUDE.md rule 2). Two bounds, because the row count and the bytes are two
// different quantities and either one alone leaves the other free:
// queryrunner.MaxExportRows is a LIMIT inside the cursor's own SELECT, so the
// rows past it are never read at all, and queryrunner.MaxExportBytes is
// counted off as the statements arrive.
//
// The byte budget is counted here rather than pushed into SQL because it is a
// budget for the whole download and not for one allocation: a single row is
// already bounded where its bytes arrive (CLAUDE.md rule 12) by the only thing
// that ever writes this column, sqlpolicy.MaxQueryBytes, so what is left to
// bound is the total — and a total is only known once the rows are counted.
//
// truncated says a bound bound. Returned rather than silently obeyed: the file
// this feeds is a record, and a record that quietly stops is worse than a
// short one that says where it stopped.
//
// The third bound is not here at all: how long this may hold the connection is
// the caller's deadline on ctx, because only the caller knows what it is
// waiting for (see api.exportDeadline).
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

	// One row past the bound, so that a log of exactly MaxExportRows is
	// reported whole rather than as a truncated one — the same "ask for one
	// more than you will show" the instance list uses to answer the same
	// question honestly.
	//
	// The LIMIT is a second lock on the door exportBudget already holds, and
	// it is deliberate that no test can tell them apart: a cursor is lazy, so
	// the rows past the last FETCH are never produced whether or not the
	// planner was told about the bound. It stays because the read this
	// replaced was a plain SELECT and could be again, and a plain SELECT with
	// no LIMIT is the unbounded read this whole comment is about. The
	// operative bound today is the budget below.
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
	// Named so a second export in the same transaction — which only the tests
	// do — finds the name free rather than already taken.
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
		// A short batch is the end of the cursor: only the last FETCH of a
		// log returns fewer rows than it asked for.
		if read < exportFetch {
			return false, nil
		}
	}
}

// exportBudget is what one download may still take, in rows and in bytes of
// SQL. It is drawn down as rows arrive and reports the moment either runs out,
// so that the stream stops on the first bound to bind rather than on whichever
// one somebody thought of first.
type exportBudget struct {
	rows  int
	bytes int
	// spent says a bound bound, and is what the caller reports to the reader
	// of the file.
	spent bool
}

// take draws one statement down against both bounds and reports whether it may
// still be handed over. The row is refused rather than trimmed: a half a
// statement in a record of what somebody wrote is worse than an honest stop.
func (b *exportBudget) take(sql string) bool {
	if b.rows <= 0 || len(sql) > b.bytes {
		b.spent = true
		return false
	}
	b.rows--
	b.bytes -= len(sql)
	return true
}

// streamExportBatch hands one FETCH's worth of rows to yield and says how many it
// read. Split out so the rows of a batch are closed on every path out of the
// loop, including a yield that refuses one.
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
			// The caller's own error, unwrapped: it is theirs to recognise.
			return read, err
		}
	}
	if err := rows.Err(); err != nil {
		return read, fmt.Errorf("read a page of the query log: %w", err)
	}
	return read, nil
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
