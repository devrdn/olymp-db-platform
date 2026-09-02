package queryrunner_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// recorder is the query log, in memory. The SQL that writes it is tested in
// internal/postgres, where it lives; what is under test here is the ordering —
// what gets written, when, and whether the answer survives a journal that
// cannot be written.
type recorder struct {
	mu sync.Mutex

	entries   []queryrunner.Entry
	outcomes  map[int64]queryrunner.Outcome
	next      int64
	beginErr  error
	finishErr error
	// order records the sequence of calls, so a test can insist that the row
	// exists before the query runs rather than after.
	order []string
}

func newRecorder() *recorder {
	return &recorder{outcomes: map[int64]queryrunner.Outcome{}}
}

func (r *recorder) Begin(_ context.Context, entry queryrunner.Entry) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.order = append(r.order, "begin")
	if r.beginErr != nil {
		return 0, r.beginErr
	}
	r.next++
	r.entries = append(r.entries, entry)
	return r.next, nil
}

func (r *recorder) Complete(_ context.Context, id int64, outcome queryrunner.Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.order = append(r.order, "complete")
	if r.finishErr != nil {
		return r.finishErr
	}
	r.outcomes[id] = outcome
	return nil
}

func journalled(t *testing.T, limits queryrunner.Limits, checker *sqlpolicy.Checker) (*queryrunner.Journalled, *recorder, string) {
	t.Helper()

	runner, database := setupWith(t, limits, checker)
	rec := newRecorder()
	return queryrunner.NewJournalled(runner, rec, slog.New(slog.NewTextHandler(io.Discard, nil))), rec, database
}

func TestTheRowIsWrittenBeforeTheQueryRuns(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`), uuid.New()); err != nil {
		t.Fatalf("running: %v", err)
	}

	if len(rec.order) != 2 || rec.order[0] != "begin" || rec.order[1] != "complete" {
		t.Fatalf("call order = %v, want begin then complete", rec.order)
	}
	if len(rec.entries) != 1 || rec.entries[0].SQL != `SELECT 1` {
		t.Fatalf("entries = %+v", rec.entries)
	}
	if got := rec.outcomes[1]; got.Status != queryrunner.StatusOK || got.Rows != 1 {
		t.Fatalf("outcome = %+v", got)
	}
}

// Everything is recorded, including what never reached the database. That is
// half the point: the journal is the record of what a participant tried, not
// only of what worked.
func TestHowEachEndingIsRecorded(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 400 * time.Millisecond

	for name, given := range map[string]struct {
		sql  string
		want queryrunner.Status
	}{
		"a query that worked":          {`SELECT 1`, queryrunner.StatusOK},
		"a query the checker refused":  {`DROP TABLE evidence`, queryrunner.StatusRejected},
		"a query the database refused": {`SELECT * FROM nothing_here`, queryrunner.StatusError},
		"a query that ran too long":    {`SELECT pg_sleep(30)`, queryrunner.StatusTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			runner, rec, database := journalled(t, limits, sqlpolicy.NewChecker("pg_sleep"))

			_, _ = runner.Run(t.Context(), request(database, given.sql), uuid.New())

			if got := rec.outcomes[1].Status; got != given.want {
				t.Fatalf("status = %q, want %q (error was %q)", got, given.want, rec.outcomes[1].Error)
			}
			if given.want != queryrunner.StatusOK && rec.outcomes[1].Error == "" {
				t.Fatal("a failure was recorded without saying what it was")
			}
		})
	}
}

// A query that cannot be recorded is not run. The journal is what the report
// of "how many queries this participant needed" is built from, and an
// execution missing from it is a quietly wrong answer later.
func TestAQueryThatCannotBeRecordedDoesNotRun(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())
	rec.beginErr = errors.New("the core database is unreachable")

	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`), uuid.New()); err == nil {
		t.Fatal("the query ran without being recorded")
	}
	if len(rec.order) != 1 {
		t.Fatalf("call order = %v; nothing should follow a failed begin", rec.order)
	}
}

// Failing to *close* the row is the other way round: the query has already
// run, and the participant is owed the answer. The row is left for the
// sweeper, which is why it was written first.
func TestAnAnswerSurvivesAJournalThatCannotBeClosed(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())
	rec.finishErr = errors.New("the core database went away")

	result, err := runner.Run(t.Context(), request(database, `SELECT 1`), uuid.New())
	if err != nil {
		t.Fatalf("the answer was lost with the journal: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d", len(result.Rows))
	}
}

// A participant who navigates away cancels the request's context. The query
// may well have finished; the row must not be left saying `running` because
// nobody was still listening for the answer.
func TestTheRowIsClosedEvenWhenTheCallerHasGoneAway(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

	ctx, cancel := context.WithCancel(t.Context())
	_, _ = runner.Run(ctx, request(database, `SELECT 1`), uuid.New())
	cancel()

	if _, closed := rec.outcomes[1]; !closed {
		t.Fatal("the row was left open")
	}
}
