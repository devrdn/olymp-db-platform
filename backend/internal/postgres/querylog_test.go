package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
)

func TestQueryLogRecordsAQueryInTwoPhases(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		id, err := log.Begin(ctx, queryrunner.Entry{
			Registration: registration,
			RequestID:    uuid.New(),
			SQL:          `SELECT * FROM suspects`,
		})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		// Before the result is known, the row already says the query happened.
		// That is the whole reason for two phases: a process that dies here
		// leaves evidence rather than nothing.
		if got := statusOf(t, ctx, id); got != "running" {
			t.Fatalf("status = %q, want running", got)
		}

		if err := log.Complete(ctx, id, queryrunner.Outcome{
			Status:   queryrunner.StatusOK,
			Duration: 1500 * time.Millisecond,
			Rows:     42,
		}); err != nil {
			t.Fatalf("complete: %v", err)
		}

		var status string
		var duration, rows int
		var completed *time.Time
		var errorText *string
		err = storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT status, duration_ms, row_count, completed_at, error_text
			 FROM query_log WHERE id = $1`, id).
			Scan(&status, &duration, &rows, &completed, &errorText)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}

		if status != "ok" || duration != 1500 || rows != 42 {
			t.Fatalf("status=%q duration=%d rows=%d", status, duration, rows)
		}
		if completed == nil {
			t.Fatal("a completed row has no completion time")
		}
		// An empty message must not become an empty string in the column: the
		// journal panel filters on "has an error", and "" is not one.
		if errorText != nil {
			t.Fatalf("error_text = %q, want NULL", *errorText)
		}
	})
}

// The row says where the query came from and what it was, normalised, in
// the same insert that opens it (design §2.3 and §5): the organiser's query
// tab shows the address, and "the same query as another participant" is
// counted on the fingerprint.
func TestQueryLogRecordsWhereAQueryCameFromAndItsFingerprint(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		sql := "SELECT name\n  FROM Suspects"
		id, err := log.Begin(ctx, queryrunner.Entry{
			Registration: someRegistration(t, ctx),
			RequestID:    uuid.New(),
			SQL:          sql,
			Address:      netip.MustParseAddr("2001:db8::42"),
		})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		var ip *netip.Addr
		var fingerprint *int64
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT ip, sql_fingerprint FROM query_log WHERE id = $1`, id).Scan(&ip, &fingerprint); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if ip == nil || *ip != netip.MustParseAddr("2001:db8::42") {
			t.Fatalf("ip = %v, want 2001:db8::42", ip)
		}
		if fingerprint == nil || *fingerprint != monitor.Fingerprint(`select name from suspects`) {
			t.Fatalf("sql_fingerprint = %v, want the fingerprint of the normalised text", fingerprint)
		}
	})
}

// An address that could not be worked out is no address, not 0.0.0.0.
func TestQueryLogStoresNoAddressWhenThereIsNone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		id := openRow(t, ctx, NewQueryLog(testPool))

		var ip *netip.Addr
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT ip FROM query_log WHERE id = $1`, id).Scan(&ip); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if ip != nil {
			t.Fatalf("ip = %v, want NULL", *ip)
		}
	})
}

func TestQueryLogKeepsTheReasonAFailureGave(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		id := openRow(t, ctx, log)

		if err := log.Complete(ctx, id, queryrunner.Outcome{
			Status: queryrunner.StatusRejected,
			Error:  "function_not_supported: pg_sleep",
		}); err != nil {
			t.Fatalf("complete: %v", err)
		}

		var status string
		var text *string
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT status, error_text FROM query_log WHERE id = $1`, id).Scan(&status, &text); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if status != "rejected" {
			t.Fatalf("status = %q", status)
		}
		if text == nil || *text != "function_not_supported: pg_sleep" {
			t.Fatalf("error_text = %v; the reason is what an operator acts on", text)
		}
	})
}

// Every status the runner can produce has to satisfy the column's check
// constraint. The two lists are in different languages — Go constants and a
// SQL CHECK — and nothing but this connects them.
func TestEveryStatusTheRunnerProducesIsAcceptedByTheColumn(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)

		for _, status := range []queryrunner.Status{
			queryrunner.StatusOK,
			queryrunner.StatusRejected,
			queryrunner.StatusError,
			queryrunner.StatusTimeout,
		} {
			id := openRow(t, ctx, log)
			if err := log.Complete(ctx, id, queryrunner.Outcome{Status: status}); err != nil {
				t.Fatalf("status %q is not accepted by the column: %v", status, err)
			}
		}
	})
}

// Closing a row that does not exist is a bug in the caller, and a silent
// no-op would hide it until somebody wondered why the journal had gaps.
func TestClosingARowThatIsNotThereIsAnError(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		err := NewQueryLog(testPool).Complete(ctx, -1, queryrunner.Outcome{Status: queryrunner.StatusOK})
		if err == nil {
			t.Fatal("closing a missing row succeeded")
		}
	})
}

// A row left at `running` is indistinguishable from a query still in flight,
// so the sweeper is what turns a crash into a recorded failure — and the
// cut-off is what keeps it from marking queries that are merely slow.
func TestTheSweeperClosesAbandonedRowsAndLeavesFreshOnes(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)

		abandoned := openRow(t, ctx, log)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE query_log SET executed_at = now() - interval '1 hour' WHERE id = $1`,
			abandoned); err != nil {
			t.Fatalf("ageing the row: %v", err)
		}
		fresh := openRow(t, ctx, log)

		swept, err := log.SweepAbandoned(ctx, time.Minute)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if swept < 1 {
			t.Fatalf("swept %d rows, want at least the abandoned one", swept)
		}

		if got := statusOf(t, ctx, abandoned); got != "error" {
			t.Fatalf("the abandoned row is %q, want error", got)
		}
		if got := statusOf(t, ctx, fresh); got != "running" {
			t.Fatalf("a query still in flight was marked %q", got)
		}
	})
}

// History is the participant's own read of the log this file otherwise only
// writes to. The one guarantee worth a test of its own: it never returns
// another registration's rows, which is the whole of what makes this safe to
// expose to a participant at all.
func TestHistoryReturnsOnlyThisRegistrationsOwnRows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		mine := someRegistration(t, ctx)
		someoneElses := someRegistration(t, ctx)

		completeRow(t, ctx, log, mine, "SELECT * FROM suspects", queryrunner.StatusOK, 12, 250)
		completeRow(t, ctx, log, someoneElses, "SELECT * FROM secrets", queryrunner.StatusOK, 1, 10)

		found, total, err := log.History(ctx, mine, 0, 0)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if total != 1 || len(found) != 1 {
			t.Fatalf("found = %d, total = %d, want exactly the one row this registration owns", len(found), total)
		}
		if found[0].SQL != "SELECT * FROM suspects" {
			t.Fatalf("SQL = %q, want this registration's own statement", found[0].SQL)
		}
	})
}

// Newest first, and duration_ms/row_count round-trip as the actual numbers
// Complete recorded — a participant reading their own log is reading the same
// facts the journal exists to keep.
func TestHistoryOrdersNewestFirstAndCarriesTheRecordedOutcome(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		completeRow(t, ctx, log, registration, "SELECT 1", queryrunner.StatusOK, 10, 3)
		// A distinct executed_at is what newest-first actually orders by; the
		// two rows would otherwise land in the same instant and the ordering
		// this test checks would be unproven.
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE query_log SET executed_at = executed_at - interval '1 minute' WHERE registration_id = $1`,
			registration); err != nil {
			t.Fatalf("backdating the first row: %v", err)
		}
		completeRow(t, ctx, log, registration, "SELECT 2", queryrunner.StatusRejected, 0, 0)

		found, total, err := log.History(ctx, registration, 0, 0)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if total != 2 || len(found) != 2 {
			t.Fatalf("found = %d, total = %d, want 2", len(found), total)
		}
		if found[0].SQL != "SELECT 2" || found[1].SQL != "SELECT 1" {
			t.Fatalf("order = [%q, %q], want the newest row first", found[0].SQL, found[1].SQL)
		}
		if found[1].Status != queryrunner.StatusOK || found[1].DurationMs == nil || *found[1].DurationMs != 3 ||
			found[1].RowCount == nil || *found[1].RowCount != 10 {
			t.Fatalf("row = %+v, want the outcome Complete recorded for it", found[1])
		}
	})
}

// A row a crashed process never closed is still this registration's own — the
// participant asked it, and the log says so even while it is stuck at
// running, with no duration or row count to report yet.
func TestHistoryCarriesARowStillRunningWithNoDurationOrRowCount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)
		if _, err := log.Begin(ctx, queryrunner.Entry{
			Registration: registration, RequestID: uuid.New(), SQL: "SELECT pg_sleep(5)",
		}); err != nil {
			t.Fatalf("begin: %v", err)
		}

		found, _, err := log.History(ctx, registration, 0, 0)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("found = %d, want exactly the one row for this registration", len(found))
		}
		if found[0].Status != queryrunner.StatusRunning || found[0].DurationMs != nil || found[0].RowCount != nil {
			t.Fatalf("row = %+v, want status=running with no duration or row count yet", found[0])
		}
	})
}

// limit and offset actually page: the second page picks up exactly where the
// first left off, with nothing repeated and nothing skipped.
func TestHistoryPagesWithLimitAndOffset(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		// Inserted oldest to newest, and spaced a minute apart so executed_at
		// alone (not insertion order) is what History actually orders by.
		statements := []string{"SELECT 1", "SELECT 2", "SELECT 3"}
		for i, sql := range statements {
			completeRow(t, ctx, log, registration, sql, queryrunner.StatusOK, 1, 1)
			minutesAgo := len(statements) - i
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE query_log SET executed_at = executed_at - ($2 * interval '1 minute')
				 WHERE registration_id = $1 AND sql_text = $3`,
				registration, minutesAgo, sql); err != nil {
				t.Fatalf("spacing out row %d: %v", i, err)
			}
		}

		first, total, err := log.History(ctx, registration, 2, 0)
		if err != nil {
			t.Fatalf("History (page 1): %v", err)
		}
		if total != 3 || len(first) != 2 {
			t.Fatalf("page 1 = %d rows, total = %d, want 2 rows of 3", len(first), total)
		}
		if first[0].SQL != "SELECT 3" || first[1].SQL != "SELECT 2" {
			t.Fatalf("page 1 order = [%q, %q]", first[0].SQL, first[1].SQL)
		}

		second, _, err := log.History(ctx, registration, 2, 2)
		if err != nil {
			t.Fatalf("History (page 2): %v", err)
		}
		if len(second) != 1 || second[0].SQL != "SELECT 1" {
			t.Fatalf("page 2 = %+v, want exactly the oldest row", second)
		}
	})
}

// One page is bounded in rows and has to be bounded in bytes too: two
// hundred rows of a 64 KiB statement each is a twelve-megabyte answer to a
// request a participant can repeat as often as their rate budget allows. The
// cut is made by the SELECT, so those bytes never leave the server, and the
// row says it was cut rather than handing somebody a silently shortened copy
// of their own query.
func TestHistoryCutsAStatementTooLongForOnePageAndSaysSo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		long := "SELECT '" + strings.Repeat("x", queryrunner.MaxHistorySQLChars) + "'"
		completeRow(t, ctx, log, registration, long, queryrunner.StatusOK, 1, 1)

		found, _, err := log.History(ctx, registration, 0, 0)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("read %d rows, want 1", len(found))
		}
		if got := utf8.RuneCountInString(found[0].SQL); got != queryrunner.MaxHistorySQLChars {
			t.Fatalf("the page carried %d characters of the statement, want the bound of %d",
				got, queryrunner.MaxHistorySQLChars)
		}
		if !found[0].SQLTruncated {
			t.Fatal("cut the participant's own statement down without saying so")
		}
		if !strings.HasPrefix(long, found[0].SQL) {
			t.Fatal("what came back is not the beginning of what was run")
		}
	})
}

// A statement that fits comes back whole and unflagged. Without this the
// truncation could be unconditional — every row shortened by a character and
// every row claiming it was cut — and the test above would not notice.
func TestHistoryLeavesAStatementThatFitsAlone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		// Exactly the bound: the last statement that is not too long.
		exact := strings.Repeat("y", queryrunner.MaxHistorySQLChars)
		completeRow(t, ctx, log, registration, exact, queryrunner.StatusOK, 1, 1)

		found, _, err := log.History(ctx, registration, 0, 0)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("read %d rows, want 1", len(found))
		}
		if found[0].SQL != exact {
			t.Fatalf("a statement of exactly the bound came back as %d characters",
				utf8.RuneCountInString(found[0].SQL))
		}
		if found[0].SQLTruncated {
			t.Fatal("a statement that fits was reported as truncated")
		}
	})
}

// The export is the record, and a record with the statements cut out of it is
// not one. It streams row by row, so a long statement costs one row's memory
// rather than a page of them — which is why the bound the page needs is not a
// bound this needs.
func TestExportHistoryCarriesEveryStatementWhole(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		long := "SELECT '" + strings.Repeat("z", queryrunner.MaxHistorySQLChars) + "'"
		completeRow(t, ctx, log, registration, long, queryrunner.StatusOK, 1, 1)

		var found []queryrunner.HistoryEntry
		if _, err := log.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
			found = append(found, entry)
			return nil
		}); err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("streamed %d rows, want 1", len(found))
		}
		if found[0].SQL != long {
			t.Fatalf("the export carried %d characters of a %d-character statement",
				utf8.RuneCountInString(found[0].SQL), utf8.RuneCountInString(long))
		}
		if found[0].SQLTruncated {
			t.Fatal("the export flagged a statement it carried whole")
		}
	})
}

// The count is a fact about the log, not a by-product of the page. Computed
// under the LIMIT it is whatever the returned rows happened to carry, so a
// page that lands past the last row reports a total of nothing — and the
// panel, which decides whether to offer "load more" by comparing what it
// holds against that number, is told the participant has never run a query.
func TestHistoryCountsEveryRowEvenOnAPageThatLandsPastTheEnd(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		for _, sql := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
			completeRow(t, ctx, log, registration, sql, queryrunner.StatusOK, 1, 1)
		}

		found, total, err := log.History(ctx, registration, 2, 10)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(found) != 0 {
			t.Fatalf("a page past the end returned %d rows, want none", len(found))
		}
		if total != 3 {
			t.Fatalf("total = %d, want 3 — the count came from the page rather than the log", total)
		}
	})
}

// completeRow opens and closes one row in a single call, for tests that only
// care about the finished result.
func completeRow(t *testing.T, ctx context.Context, log *QueryLog, registration uuid.UUID, sql string, status queryrunner.Status, rows, durationMs int) {
	t.Helper()

	id, err := log.Begin(ctx, queryrunner.Entry{Registration: registration, RequestID: uuid.New(), SQL: sql})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := log.Complete(ctx, id, queryrunner.Outcome{
		Status: status, Rows: rows, Duration: time.Duration(durationMs) * time.Millisecond,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func someRegistration(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()

	user := makeUser(t, ctx, "querylog-"+uuid.NewString()[:8])
	return makeRegistration(t, ctx, makeContest(t, ctx, user.ID), user.ID)
}

func openRow(t *testing.T, ctx context.Context, log *QueryLog) int64 {
	t.Helper()

	id, err := log.Begin(ctx, queryrunner.Entry{
		Registration: someRegistration(t, ctx),
		RequestID:    uuid.New(),
		SQL:          `SELECT 1`,
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return id
}

func statusOf(t *testing.T, ctx context.Context, id int64) string {
	t.Helper()

	var status string
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT status FROM query_log WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// ExportHistory is the streamed read behind the participant's CSV download.
// The same guarantee History carries, and the one that makes this endpoint
// safe to hand a participant at all: it never yields another registration's
// rows. Asserted here rather than trusted to look right, because the CSV path
// writes what it is given straight to the socket and there is no page size
// left to notice a stranger's statement in.
func TestExportHistoryStreamsOnlyThisRegistrationsOwnRows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		mine := someRegistration(t, ctx)
		someoneElses := someRegistration(t, ctx)

		completeRow(t, ctx, log, mine, "SELECT * FROM suspects", queryrunner.StatusOK, 12, 250)
		completeRow(t, ctx, log, someoneElses, "SELECT * FROM secrets", queryrunner.StatusOK, 1, 10)

		var seen []string
		if _, err := log.ExportHistory(ctx, mine, func(entry queryrunner.HistoryEntry) error {
			seen = append(seen, entry.SQL)
			return nil
		}); err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if len(seen) != 1 || seen[0] != "SELECT * FROM suspects" {
			t.Fatalf("streamed %v, want exactly this registration's own statement", seen)
		}
	})
}

// Oldest first, and unpaged: the file is a record of one session, read top to
// bottom, and every row of it is in there — see ExportHistory's own doc.
func TestExportHistoryStreamsTheWholeLogOldestFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		statements := []string{"SELECT 1", "SELECT 2", "SELECT 3"}
		for i, sql := range statements {
			completeRow(t, ctx, log, registration, sql, queryrunner.StatusOK, 1, 1)
			minutesAgo := len(statements) - i
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE query_log SET executed_at = executed_at - ($2 * interval '1 minute')
				 WHERE registration_id = $1 AND sql_text = $3`,
				registration, minutesAgo, sql); err != nil {
				t.Fatalf("spacing out row %d: %v", i, err)
			}
		}

		var seen []string
		if _, err := log.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
			seen = append(seen, entry.SQL)
			return nil
		}); err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if len(seen) != 3 || seen[0] != "SELECT 1" || seen[2] != "SELECT 3" {
			t.Fatalf("streamed %v, want every row oldest first", seen)
		}
	})
}

// A yield that fails stops the stream and surfaces: the caller is writing to
// a socket, and a client that hung up must not have the rest of the log read
// out of the database on its behalf.
func TestExportHistoryStopsWhenTheCallerCannotTakeAnotherRow(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)
		completeRow(t, ctx, log, registration, "SELECT 1", queryrunner.StatusOK, 1, 1)
		completeRow(t, ctx, log, registration, "SELECT 2", queryrunner.StatusOK, 1, 1)

		broken := errors.New("the client hung up")
		seen := 0
		_, err := log.ExportHistory(ctx, registration, func(queryrunner.HistoryEntry) error {
			seen++
			return broken
		})
		if !errors.Is(err, broken) {
			t.Fatalf("ExportHistory returned %v, want the caller's own error", err)
		}
		if seen != 1 {
			t.Fatalf("kept going for %d rows after the caller refused one", seen)
		}
	})
}

// committedRegistration enrols somebody for real — outside any transaction —
// and removes them again when the test ends.
//
// Every other test in this file hangs its fixtures off withTx and lets the
// rollback clean up, which is right for them and useless for a test whose
// whole subject is what happens when there is no ambient transaction. The
// deletes cascade: dropping the contest takes the registration, and the
// registration takes its query_log rows.
func committedRegistration(t *testing.T) (context.Context, uuid.UUID) {
	t.Helper()
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()

	user := makeUser(t, ctx, "querylog-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, user.ID)
	registration := makeRegistration(t, ctx, contest, user.ID)

	t.Cleanup(func() {
		clean := context.Background()
		if _, err := testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest); err != nil {
			t.Errorf("clean up the contest: %v", err)
		}
		if _, err := testPool.Exec(clean, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Errorf("clean up the user: %v", err)
		}
	})
	return ctx, registration
}

// The export on the path the deployment actually uses: no transaction around
// it (CLAUDE.md rule 10).
//
// queryLogCSV calls ExportHistory straight off the request context, and the
// cursor it reads through can only be declared inside a transaction — so the
// method opens one when the caller has none. Every other test here runs
// inside withTx, which hands it a transaction for free and would hide a
// missing BEGIN completely.
//
// The log is deliberately longer than one FETCH (exportFetch rows), and not a
// whole number of them, so the loop that walks the cursor is walked more than
// once and the last batch is a short one — the condition that ends it.
func TestExportHistoryStreamsTheWholeLogWithNoTransactionAroundIt(t *testing.T) {
	ctx, registration := committedRegistration(t)
	log := NewQueryLog(testPool)

	const rows = 2*exportFetch + 1
	want := make([]string, 0, rows)
	for i := range rows {
		sql := fmt.Sprintf("SELECT %d", i)
		want = append(want, sql)
		completeRow(t, ctx, log, registration, sql, queryrunner.StatusOK, 1, 1)
	}
	// One distinct executed_at per row, ascending in insertion order, so the
	// order asserted below is a real ordering rather than a tie the rows
	// happened to come back in.
	if _, err := testPool.Exec(ctx, `
		UPDATE query_log
		SET executed_at = now() - make_interval(secs => (SELECT max(id) FROM query_log WHERE registration_id = $1) - id)
		WHERE registration_id = $1`, registration); err != nil {
		t.Fatalf("space out the rows: %v", err)
	}

	var seen []string
	if _, err := log.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
		seen = append(seen, entry.SQL)
		return nil
	}); err != nil {
		t.Fatalf("ExportHistory with no ambient transaction: %v", err)
	}

	if len(seen) != rows {
		t.Fatalf("streamed %d rows, want all %d — a log cut at a FETCH boundary is a record missing most of itself", len(seen), rows)
	}
	for i, sql := range want {
		if seen[i] != sql {
			t.Fatalf("row %d is %q, want %q: the batches did not come back oldest first", i, seen[i], sql)
		}
	}
}

// bulkRows writes count rows for one registration in a single statement, each
// carrying `bytes` characters of SQL and a distinct executed_at so the
// export's own ordering is well defined.
//
// One INSERT rather than count calls to Begin: the bounds below are only
// interesting at tens of thousands of rows, and twenty thousand round trips
// would make the test the slowest thing in the package.
func bulkRows(t *testing.T, ctx context.Context, registration uuid.UUID, count, bytes int) {
	t.Helper()

	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
		INSERT INTO query_log (registration_id, request_id, sql_text, status, executed_at)
		SELECT $1, gen_random_uuid(), repeat('x', $3), 'ok', now() + (g * interval '1 millisecond')
		FROM generate_series(1, $2) AS g`,
		registration, count, bytes); err != nil {
		t.Fatalf("writing %d rows: %v", count, err)
	}
}

// The export streams, which was taken to mean it needed no bound; those are
// different things (CLAUDE.md rule 2). The rows past the bound are never read
// at all — the LIMIT is inside the cursor's own SELECT — and the caller is
// told, so the file can say where it stopped.
func TestExportHistoryStopsAtTheRowBoundAndSaysSo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)
		bulkRows(t, ctx, registration, queryrunner.MaxExportRows+10, 8)

		streamed := 0
		truncated, err := log.ExportHistory(ctx, registration, func(queryrunner.HistoryEntry) error {
			streamed++
			return nil
		})
		if err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if streamed != queryrunner.MaxExportRows {
			t.Fatalf("streamed %d rows, want the bound of %d", streamed, queryrunner.MaxExportRows)
		}
		if !truncated {
			t.Fatal("a log longer than one download may carry was reported as complete")
		}
	})
}

// And a log of exactly the bound is complete, not truncated. The cursor asks
// for one row more than it will hand over precisely so this case is answered
// honestly rather than by whichever way the comparison happened to fall.
func TestExportHistoryOfExactlyTheRowBoundIsNotTruncated(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)
		bulkRows(t, ctx, registration, queryrunner.MaxExportRows, 8)

		streamed := 0
		truncated, err := log.ExportHistory(ctx, registration, func(queryrunner.HistoryEntry) error {
			streamed++
			return nil
		})
		if err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if streamed != queryrunner.MaxExportRows || truncated {
			t.Fatalf("streamed %d rows, truncated=%v; a log of exactly the bound is whole", streamed, truncated)
		}
	})
}

// The row count alone is half a bound: sqlpolicy.MaxQueryBytes lets one
// statement be 64 KiB, so twenty thousand rows is more than a gigabyte down
// one connection. The byte budget is what makes the row count mean something,
// and it binds first when the statements are large.
func TestExportHistoryStopsAtTheByteBoundBeforeTheRowBound(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		log := NewQueryLog(testPool)
		registration := someRegistration(t, ctx)

		const each = 1 << 20
		rows := queryrunner.MaxExportBytes/each + 1
		bulkRows(t, ctx, registration, rows, each)

		streamed, carried := 0, 0
		truncated, err := log.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
			streamed++
			carried += len(entry.SQL)
			return nil
		})
		if err != nil {
			t.Fatalf("ExportHistory: %v", err)
		}
		if !truncated {
			t.Fatal("a log past the byte budget was reported as complete")
		}
		if streamed >= rows {
			t.Fatalf("streamed all %d rows; the byte budget never bound", streamed)
		}
		if carried > queryrunner.MaxExportBytes {
			t.Fatalf("carried %d bytes of SQL, over the budget of %d", carried, queryrunner.MaxExportBytes)
		}
	})
}
