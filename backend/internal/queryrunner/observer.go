package queryrunner

import (
	"context"
	"errors"
	"time"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// Observer receives the runner's decisions as it makes them, for metrics.
// Every label value it is handed is one Endings or CutOffs lists, never
// anything a request carries. Implementations must be safe for concurrent use
// and must never fail: metrics are diagnostics and cannot change an answer.
type Observer interface {
	// Ended is called once per Run, however it ended.
	Ended(outcome, reason string)
	// Waited is how long an admitted or timed-out query waited for a slot. A
	// query refused at the gate's door never waited and is not reported.
	Waited(d time.Duration)
	// Executed is how long an admitted query held its slot.
	Executed(outcome string, d time.Duration)
	// CutOff is one answer cut by a limit of the result budget.
	CutOff(limit string)
}

// Reasons Run reports beside the journal's status. A refusal by the checker
// reports its sqlpolicy code instead.
const (
	reasonRateLimited    = "rate_limited"
	reasonBusy           = "busy"
	reasonAlreadyRunning = "already_running"
	reasonDiskFull       = "disk_full"
	reasonQueue          = "queue"
	reasonDeadline       = "deadline"
	reasonCancelled      = "cancelled"
	reasonResultTooLarge = "result_too_large"
	reasonDatabase       = "database"
	reasonInternal       = "internal"
)

// The limits of the result budget that can cut an answer.
const (
	CutOffRows  = "rows"
	CutOffBytes = "bytes"
	// CutOffSocket is the read budget on the connection refusing an answer
	// before any of it could be kept (ErrResultTooLarge).
	CutOffSocket = "socket"
	// CutOffRendered is the transport's own count of the rendered answer
	// (internal/rpc), which can cut what the runner's estimate let through.
	CutOffRendered = "rendered"
)

// Endings lists every outcome Run reports with the reasons each may carry, so
// a metrics backend can declare the series up front and refuse any other.
func Endings() map[string][]string {
	rejected := []string{reasonRateLimited, reasonBusy, reasonAlreadyRunning, reasonDiskFull}
	for _, code := range sqlpolicy.Codes() {
		rejected = append(rejected, string(code))
	}
	return map[string][]string{
		string(StatusOK):       {""},
		string(StatusRejected): rejected,
		string(StatusTimeout):  {reasonQueue, reasonDeadline},
		string(StatusError):    {reasonCancelled, reasonResultTooLarge, reasonDatabase, reasonInternal},
	}
}

// CutOffs lists every limit CutOff may be handed.
func CutOffs() []string {
	return []string{CutOffRows, CutOffBytes, CutOffSocket, CutOffRendered}
}

// ending names how a Run ended: the journal's status, and a reason from
// Endings. admitted tells a timeout waiting for a slot from one past the
// deadline.
func ending(err error, admitted bool) (outcome, reason string) {
	outcome = string(statusOf(err))

	var refusal *sqlpolicy.Refusal
	var connect *pgconn.ConnectError
	var pgErr *pgconn.PgError

	switch {
	case err == nil:
		return outcome, ""
	case errors.As(err, &refusal):
		return outcome, string(refusal.Code)
	case errors.Is(err, ErrTooManyQueries):
		return outcome, reasonRateLimited
	case errors.Is(err, ErrBusy):
		return outcome, reasonBusy
	case errors.Is(err, ErrAlreadyRunning):
		return outcome, reasonAlreadyRunning
	case errors.Is(err, ErrDiskFull):
		return outcome, reasonDiskFull
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		if admitted {
			return outcome, reasonDeadline
		}
		return outcome, reasonQueue
	case errors.Is(err, ErrCanceled), errors.Is(err, context.Canceled):
		return outcome, reasonCancelled
	case errors.Is(err, ErrResultTooLarge):
		return outcome, reasonResultTooLarge
	// A refused login nests a PgError inside a ConnectError: the database
	// never saw the query, so it is ours.
	case errors.As(err, &connect):
		return outcome, reasonInternal
	case errors.As(err, &pgErr):
		return outcome, reasonDatabase
	default:
		return outcome, reasonInternal
	}
}

// discard is the Observer of a runner nobody watches.
type discard struct{}

func (discard) Ended(string, string)           {}
func (discard) Waited(time.Duration)           {}
func (discard) Executed(string, time.Duration) {}
func (discard) CutOff(string)                  {}
