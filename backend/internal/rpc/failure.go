// Package rpc carries the one contract between the Core API and the Query
// Runner (proto/queryrunner/v1).
//
// It is to gRPC what internal/api is to HTTP: the place where a transport is
// translated into the domain and back, so that neither internal/queryrunner
// nor its caller has to know a wire format exists. The server adapts an
// executor to the service; the client adapts the service back to
// queryrunner.Executor, so that what the Core API journals and calls is the
// same shape whether the runner is across a socket or in the process.
//
// Both directions of every mapping live beside each other on purpose. A
// translation written twice, once per direction, is a translation that drifts
// the first time somebody adds a case to one of them.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// failureFor turns what the runner returned into what the wire carries.
//
// Ordinary outcomes only. A refusal, a timeout and a full instance are answers
// to the question that was asked, so they travel in the response; a gRPC error
// status is reserved for the runner being unable to answer at all, which is
// the distinction that lets a caller tell "your query was refused" from "the
// query service is down".
func failureFor(err error) *pb.Failure {
	var refusal *sqlpolicy.Refusal

	switch {
	case err == nil:
		return nil
	case errors.As(err, &refusal):
		return &pb.Failure{
			Kind:    pb.Failure_KIND_REFUSED.Enum(),
			Code:    ptr(string(refusal.Code)),
			Subject: ptr(refusal.Subject),
			Message: ptr(err.Error()),
			// The character the parser objected to. It travels because the
			// checker runs on this side of the wire and the console that
			// underlines it is on the other, which made a field the console
			// already reads permanently zero (CLAUDE.md rule 11).
			//
			Position: ptr(wirePosition(refusal.Position)),
		}
	case errors.Is(err, queryrunner.ErrAlreadyRunning):
		return &pb.Failure{Kind: pb.Failure_KIND_ALREADY_RUNNING.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrBusy):
		return &pb.Failure{Kind: pb.Failure_KIND_BUSY.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrTooManyQueries):
		return &pb.Failure{Kind: pb.Failure_KIND_RATE_LIMITED.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrDiskFull):
		return &pb.Failure{Kind: pb.Failure_KIND_DISK_FULL.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrResultTooLarge):
		return &pb.Failure{Kind: pb.Failure_KIND_RESULT_TOO_LARGE.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return &pb.Failure{Kind: pb.Failure_KIND_TIMEOUT.Enum(), Message: ptr(err.Error())}
	case errors.Is(err, queryrunner.ErrCanceled), errors.Is(err, context.Canceled):
		return &pb.Failure{Kind: pb.Failure_KIND_CANCELLED.Enum(), Message: ptr(err.Error())}
	default:
		return classify(err)
	}
}

// classify decides, for an error nothing above recognised, whether the
// database refused this query or this service failed.
//
// By what the error *is*, never by what is left over. Only PostgreSQL's own
// error about a statement — a *pgconn.PgError — is the database refusing
// anything; everything else is ours, because the alternative default hands a
// participant an explanation of our infrastructure and tells them their SQL
// was wrong. The message travels in both cases: it is what the journal and the
// technical log read, and the contract says it is never repeated to the person
// asking (Failure.message).
//
// The order of the two checks is the whole of this function. A cluster that
// refuses a login answers with a PgError (SQLSTATE 28P01), and pgx hands it
// back nested inside a *pgconn.ConnectError — alongside every address it
// dialled and the database it asked for. Asking "is there a PgError in here"
// first therefore answers yes for a connection that never opened, which is how
// the game cluster's host, port and role name reached a participant's console
// under "check the fields you filled in". A connection failure is named first,
// and it is not a database error however it failed: the database never saw the
// query.
func classify(err error) *pb.Failure {
	internal := &pb.Failure{Kind: pb.Failure_KIND_INTERNAL.Enum(), Message: ptr(err.Error())}

	var connect *pgconn.ConnectError
	if errors.As(err, &connect) {
		return internal
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &pb.Failure{Kind: pb.Failure_KIND_DATABASE_ERROR.Enum(), Message: ptr(err.Error())}
	}
	return internal
}

// wirePosition narrows a parser's offset to the field that carries it.
//
// A bare conversion wraps: `Refusal.Position` is an `int` and the field is an
// `int32`, so a value past 2^31 arrives negative and the console hands a
// negative document position to the editor. Arguing it cannot get that large
// — a statement is bounded by sqlpolicy.MaxQueryBytes — is an argument about
// another package's constant, not about this line, and it stops being true
// the day that constant moves.
//
// Anything outside what the field can carry becomes "no position", which is
// what the console already renders for every refusal that names no place in
// the text. Better no underline than one under the wrong character.
func wirePosition(position int) int32 {
	if position <= 0 || position > math.MaxInt32 {
		return 0
	}
	return int32(position)
}

// errorFor turns the wire's answer back into the error the caller expects.
//
// The errors it returns are the runner package's own, so that a caller written
// against a local runner behaves the same against a remote one — including
// `errors.Is` and the refusal's code, which the interface needs in order to
// say the refusal in the participant's language.
func errorFor(failure *pb.Failure) error {
	if failure == nil {
		return nil
	}

	switch failure.GetKind() {
	case pb.Failure_KIND_REFUSED:
		return &sqlpolicy.Refusal{
			Code:    sqlpolicy.Code(failure.GetCode()),
			Subject: failure.GetSubject(),
			// A 1-based character offset, carried through as it is. The
			// handler above (api.ConsoleHandler.fail) omits the key entirely
			// when it is zero, which is how a client tells "no position" from
			// "the very first character" — so nothing here has to invent one.
			Position: int(failure.GetPosition()),
		}
	case pb.Failure_KIND_ALREADY_RUNNING:
		return queryrunner.ErrAlreadyRunning
	case pb.Failure_KIND_BUSY:
		return queryrunner.ErrBusy
	case pb.Failure_KIND_RATE_LIMITED:
		return queryrunner.ErrTooManyQueries
	case pb.Failure_KIND_DISK_FULL:
		return queryrunner.ErrDiskFull
	case pb.Failure_KIND_RESULT_TOO_LARGE:
		return queryrunner.ErrResultTooLarge
	case pb.Failure_KIND_TIMEOUT:
		return queryrunner.ErrTimeout
	case pb.Failure_KIND_CANCELLED:
		return queryrunner.ErrCanceled
	case pb.Failure_KIND_DATABASE_ERROR:
		// Named as the database's own, so that the layers above show its
		// words because they decided to and not because they ran out of
		// cases.
		return &queryrunner.DatabaseError{Message: failure.GetMessage()}
	case pb.Failure_KIND_INTERNAL:
		// ErrUnreachable rather than queryproxy.ErrUnavailable, for two
		// reasons. The Core API already answers this one with 503 and "the
		// query service is unavailable" — a generic sentence, and a status
		// that says "try again" rather than "your request was bad", which is
		// exactly right for a runner that could not reach the game cluster;
		// its 500 counterpart reads as a bug of ours that a retry will not
		// help. And the dependency: the façade consumes this package through
		// an interface it declares itself, so reaching up into it from here
		// for a sentinel would point that arrow backwards.
		//
		// The message rides along for the log — %s and not %w, because there
		// is nothing left of the far side's error to unwrap, only its text —
		// and the handler answers from the sentinel, never from this string.
		return fmt.Errorf("%w: %s", ErrUnreachable, failure.GetMessage())
	default:
		// A kind this build does not know is still a failure. Reporting it as
		// success because the enum is unfamiliar would turn a newer runner
		// into silently wrong answers.
		return errors.New("the query service refused the query: " + failure.GetMessage())
	}
}

// policyProto and policyFrom carry the contest's policy across.
//
// Mode stays a string on both sides. It is a domain constant, and an enum on
// the wire would be a second list to keep in step with the first — for a value
// whose whole job is to be compared against sqlpolicy's own.
func policyProto(p sqlpolicy.Policy) *pb.Policy {
	return &pb.Policy{
		Mode:            ptr(string(p.Mode)),
		WritableTables:  p.WritableTables,
		AllowCreateView: ptr(p.AllowCreateView),
		AllowOwnTables:  ptr(p.AllowOwnTables),
		AllowTempTables: ptr(p.AllowTempTables),
		AllowCatalog:    ptr(p.AllowCatalog),
	}
}

func policyFrom(p *pb.Policy) sqlpolicy.Policy {
	return sqlpolicy.Policy{
		Mode:            sqlpolicy.Mode(p.GetMode()),
		WritableTables:  p.GetWritableTables(),
		AllowCreateView: p.GetAllowCreateView(),
		AllowOwnTables:  p.GetAllowOwnTables(),
		AllowTempTables: p.GetAllowTempTables(),
		AllowCatalog:    p.GetAllowCatalog(),
	}
}

func ptr[T any](value T) *T { return &value }
