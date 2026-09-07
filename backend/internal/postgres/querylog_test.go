package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

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
		if err := log.ExportHistory(ctx, mine, func(entry queryrunner.HistoryEntry) error {
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
		if err := log.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
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
		err := log.ExportHistory(ctx, registration, func(queryrunner.HistoryEntry) error {
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
