package queryrunner_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

// recorder is an in-memory query log. The SQL is tested in internal/postgres;
// here the ordering is under test.
type recorder struct {
	mu sync.Mutex

	entries   []queryrunner.Entry
	outcomes  map[int64]queryrunner.Outcome
	next      int64
	beginErr  error
	finishErr error
	// order records the call sequence, to show the row exists before the
	// query runs.
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

func journalled(t *testing.T, limits queryrunner.Limits, checker *checker.Checker) (*queryrunner.Journalled, *recorder, string) {
	t.Helper()

	runner, database := setupWith(t, limits, checker)
	rec := newRecorder()
	return queryrunner.NewJournalled(runner, rec, slog.New(slog.NewTextHandler(io.Discard, nil))), rec, database
}

func TestTheRowIsWrittenBeforeTheQueryRuns(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), checker.NewChecker())

	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`), queryrunner.Origin{RequestID: uuid.New()}); err != nil {
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

// Queries that never reached the database are recorded too.
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
			runner, rec, database := journalled(t, limits, checker.NewChecker("pg_sleep"))

			_, _ = runner.Run(t.Context(), request(database, given.sql), queryrunner.Origin{RequestID: uuid.New()})

			if got := rec.outcomes[1].Status; got != given.want {
				t.Fatalf("status = %q, want %q (error was %q)", got, given.want, rec.outcomes[1].Error)
			}
			if given.want != queryrunner.StatusOK && rec.outcomes[1].Error == "" {
				t.Fatal("a failure was recorded without saying what it was")
			}
		})
	}
}

func TestAQueryThatCannotBeRecordedDoesNotRun(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), checker.NewChecker())
	rec.beginErr = errors.New("the core database is unreachable")

	_, err := runner.Run(t.Context(), request(database, `SELECT 1`), queryrunner.Origin{RequestID: uuid.New()})
	if err == nil {
		t.Fatal("the query ran without being recorded")
	}
	// Wrapped, so the caller can tell a journal failure from the database
	// refusing the participant's SQL.
	if !errors.Is(err, queryrunner.ErrJournalUnavailable) {
		t.Fatalf("error = %v, want it to wrap ErrJournalUnavailable", err)
	}
	if len(rec.order) != 1 {
		t.Fatalf("call order = %v; nothing should follow a failed begin", rec.order)
	}
}

// The query has already run, so the participant still gets the answer; the
// sweeper closes the row.
func TestAnAnswerSurvivesAJournalThatCannotBeClosed(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), checker.NewChecker())
	rec.finishErr = errors.New("the core database went away")

	result, err := runner.Run(t.Context(), request(database, `SELECT 1`), queryrunner.Origin{RequestID: uuid.New()})
	if err != nil {
		t.Fatalf("the answer was lost with the journal: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d", len(result.Rows))
	}
}

func TestTheRowIsClosedEvenWhenTheCallerHasGoneAway(t *testing.T) {
	runner, rec, database := journalled(t, queryrunner.DefaultLimits(), checker.NewChecker())

	ctx, cancel := context.WithCancel(t.Context())
	_, _ = runner.Run(ctx, request(database, `SELECT 1`), queryrunner.Origin{RequestID: uuid.New()})
	cancel()

	if _, closed := rec.outcomes[1]; !closed {
		t.Fatal("the row was left open")
	}
}

// The timeout status feeds load reports, so a caller leaving must not count.
func TestACancelledRequestIsNotJournalledAsATimeout(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 30 * time.Second
	runner, rec, database := journalled(t, limits, checker.NewChecker("pg_sleep"))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	_, _ = runner.Run(ctx, request(database, `SELECT pg_sleep(30)`), queryrunner.Origin{RequestID: uuid.New()})

	if got := rec.outcomes[1].Status; got == queryrunner.StatusTimeout {
		t.Fatal("a cancelled request was journalled as a timeout")
	} else if got != queryrunner.StatusError {
		t.Fatalf("status = %q, want %q", got, queryrunner.StatusError)
	}
}

// answering is an Executor that answers without a database.
type answering struct{}

func (answering) Run(context.Context, queryrunner.Request) (*queryrunner.Result, error) {
	return &queryrunner.Result{}, nil
}

// The address and request id travel beside the Request, not inside it, since
// the Query Runner never sees them (section 2.3, CLAUDE.md rule 11).
func TestTheRowCarriesTheOriginOfTheQuery(t *testing.T) {
	rec := newRecorder()
	journalled := queryrunner.NewJournalled(answering{}, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	origin := queryrunner.Origin{RequestID: uuid.New(), Address: netip.MustParseAddr("198.51.100.7")}

	if _, err := journalled.Run(t.Context(), queryrunner.Request{Registration: uuid.New(), SQL: `SELECT 1`}, origin); err != nil {
		t.Fatalf("running: %v", err)
	}

	if len(rec.entries) != 1 {
		t.Fatalf("journalled %d rows, want 1", len(rec.entries))
	}
	entry := rec.entries[0]
	if entry.RequestID != origin.RequestID || entry.Address != origin.Address {
		t.Fatalf("entry = %+v, want request %s from %v", entry, origin.RequestID, origin.Address)
	}
}
