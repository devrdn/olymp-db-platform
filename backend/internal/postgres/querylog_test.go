package postgres

import (
	"context"
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
