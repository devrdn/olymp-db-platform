package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
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
	// RequestID ties this row to the same request in the technical logs.
	RequestID uuid.UUID
	SQL       string
	// Address is where the query came from; the zero value records none.
	Address netip.Addr
}

// Origin is what the Core API knows about the request that carried a query.
// It goes into the journal and never crosses the gRPC contract, since the
// journal is written on the Core API's side.
type Origin struct {
	RequestID uuid.UUID
	// Address is the exact client address from httpx.ClientIP, not the rate
	// limiter's grouped subject. The zero value means unknown.
	Address netip.Addr
}

// ErrJournalUnavailable wraps a failure to open the journal row. It is marked
// apart because it is our infrastructure failing, not the participant's SQL,
// and the database's words about it must not reach the participant.
var ErrJournalUnavailable = errors.New("the query could not be journalled")

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

// Executor is the part of a runner the journal wraps: a local Runner or a
// client of the Query Runner service (section 2.3).
type Executor interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// Journalled is an Executor that writes the query log around each execution
// (section 5, point 7). The row is written before the query is sent, so a
// process that dies mid-query still leaves a `running` row for the sweeper to
// close as `error`; the outcome is filled in afterwards.
type Journalled struct {
	runner  Executor
	journal Journal
	log     *slog.Logger
}

// NewJournalled wraps an executor.
func NewJournalled(runner Executor, journal Journal, log *slog.Logger) *Journalled {
	return &Journalled{runner: runner, journal: journal, log: log}
}

// Run records the query, runs it, and records how it ended.
//
// A query that cannot be recorded is not run: reports count queries from the
// journal, and the core database is needed for everything else anyway. A row
// that cannot be closed is only logged, since the participant is owed the
// answer; the sweeper closes it.
func (j *Journalled) Run(ctx context.Context, req Request, origin Origin) (*Result, error) {
	id, err := j.journal.Begin(ctx, Entry{
		Registration: req.Registration,
		RequestID:    origin.RequestID,
		SQL:          req.SQL,
		Address:      origin.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJournalUnavailable, err)
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
		// A write answering with a count records that count.
		if outcome.Rows == 0 && result.RowsAffected > 0 {
			outcome.Rows = int(min(result.RowsAffected, int64(math.MaxInt32)))
		}
	}

	// Detached so a caller who navigated away does not leave the row at
	// `running`.
	closing, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := j.journal.Complete(closing, id, outcome); err != nil {
		j.log.ErrorContext(ctx, "could not close a query log row",
			"error", err, "query_log_id", id, "status", outcome.Status)
	}

	return result, runErr
}

// statusOf maps what happened to what the journal records.
func statusOf(err error) Status {
	var refusal *sqlpolicy.Refusal

	switch {
	case err == nil:
		return StatusOK
	case errors.As(err, &refusal):
		return StatusRejected
	case errors.Is(err, ErrBusy), errors.Is(err, ErrAlreadyRunning),
		errors.Is(err, ErrTooManyQueries), errors.Is(err, ErrDiskFull):
		// Declined like a validator refusal; the message says which.
		return StatusRejected
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	case errors.Is(err, ErrCanceled), errors.Is(err, context.Canceled):
		// Not a timeout: the caller left, and the query's fate is unknown.
		return StatusError
	default:
		return StatusError
	}
}
