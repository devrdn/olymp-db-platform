package queryrunner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Status is how a query ended, in the journal's vocabulary.
type Status string

const (
	StatusRunning  Status = "running"
	StatusOK       Status = "ok"
	StatusRejected Status = "rejected"
	StatusError    Status = "error"
	StatusTimeout  Status = "timeout"
)

// Entry opens a journal row, before the query runs.
type Entry struct {
	Registration uuid.UUID
	// RequestID ties this row to the same request in the technical logs, which
	// is what makes "the participant says it failed at 14:02" answerable.
	RequestID uuid.UUID
	SQL       string
}

// Outcome closes it.
type Outcome struct {
	Status   Status
	Error    string
	Duration time.Duration
	Rows     int
}

// Journal is the part of the query log this package needs.
type Journal interface {
	// Begin records a query that is about to run and returns the row's id.
	Begin(ctx context.Context, entry Entry) (int64, error)
	// Complete fills in how it ended.
	Complete(ctx context.Context, id int64, outcome Outcome) error
}

// Executor is the part of a runner the journal wraps.
//
// An interface with one method, and it is the seam the architecture turns on:
// the Query Runner is a separate service (section 2.3), so what the Core API
// journals is a call over the network, not a local execution. Both satisfy
// this, which is why moving the runner out of the process changes a line in
// the composition root and nothing here.
type Executor interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// Journalled is an Executor that writes the query log around each execution.
//
// Two phases, and the order is the point (section 5, point 7). The row is
// written *before* the query is sent, so that a process that dies mid-query
// still leaves evidence that the query happened; the result is filled in
// afterwards by an update. Rows left at `running` by a crash are visible and
// are swept to `error` by a background job — which only works because they
// were written first.
//
// It wraps the executor rather than living inside it, so that the runner stays
// the thing that answers and this stays the thing that records — and so that
// it can wrap the service client just as readily as a local runner. Section 2
// puts the query log on the Core API's side of that boundary, which is only
// possible if this does not know which side it is on.
type Journalled struct {
	runner  Executor
	journal Journal
	log     *slog.Logger
}

// NewJournalled wraps an executor — a local Runner, or a client of the Query
// Runner service.
func NewJournalled(runner Executor, journal Journal, log *slog.Logger) *Journalled {
	return &Journalled{runner: runner, journal: journal, log: log}
}

// Run records the query, runs it, and records how it ended.
//
// A query that cannot be recorded is not run. That is a deliberate trade: the
// journal is not only an audit trail, it is the material the report of "how
// many queries this participant needed" is built from, and an execution
// missing from it is a quietly wrong answer later. The interface already
// depends on the core database being reachable for everything else, so
// refusing here costs no availability that was not already spent.
//
// A query that cannot be *closed* is different: it has already run, and the
// participant is owed the answer. The failure is logged and the row is left
// for the sweeper.
func (j *Journalled) Run(ctx context.Context, req Request, requestID uuid.UUID) (*Result, error) {
	id, err := j.journal.Begin(ctx, Entry{
		Registration: req.Registration,
		RequestID:    requestID,
		SQL:          req.SQL,
	})
	if err != nil {
		return nil, err
	}

	started := time.Now()
	result, runErr := j.runner.Run(ctx, req)
	outcome := Outcome{
		Status:   statusOf(runErr),
		Duration: time.Since(started),
	}
	if runErr != nil {
		outcome.Error = runErr.Error()
	}
	if result != nil {
		outcome.Rows = len(result.Rows)
	}

	// Detached from the request's context on purpose: a participant who
	// navigated away cancels the context, and the row would then stay at
	// `running` for a query that finished perfectly well.
	closing, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := j.journal.Complete(closing, id, outcome); err != nil {
		j.log.ErrorContext(ctx, "could not close a query log row",
			"error", err, "query_log_id", id, "status", outcome.Status)
	}

	return result, runErr
}

// statusOf maps what happened to what the journal records.
//
// Load shedding lands on `rejected` along with a validator's refusal, and the
// two are told apart by the recorded message rather than by the status. Both
// mean the same thing to a reader of the log — the system declined to run it,
// and nothing reached the database — and inventing a status for the rarer one
// would mean a schema change for a distinction the message already makes.
func statusOf(err error) Status {
	var refusal *sqlpolicy.Refusal

	switch {
	case err == nil:
		return StatusOK
	case errors.As(err, &refusal):
		return StatusRejected
	case errors.Is(err, ErrBusy), errors.Is(err, ErrAlreadyRunning):
		return StatusRejected
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	case errors.Is(err, ErrCanceled), errors.Is(err, context.Canceled):
		// Not a timeout: the query did not run out of time, the caller stopped
		// waiting, and what happened to the query itself is unknown. `error`
		// with the reason recorded is the honest answer, and it keeps the
		// timeout count meaning what a reader assumes it means.
		return StatusError
	default:
		return StatusError
	}
}
