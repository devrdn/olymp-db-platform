// Package rpc carries the contract between the Core API and the Query Runner
// (proto/queryrunner/v1). It is to gRPC what internal/api is to HTTP: the
// server adapts a queryrunner.Executor to the service, the client adapts the
// service back. It does not run queries or decide policy. Both directions of
// every mapping live side by side so they cannot drift apart.
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

// failureFor turns what the runner returned into what the wire carries. A
// refusal, a timeout or a full instance is an answer and travels in the
// response; a gRPC status means the runner could not answer at all.
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
			// The checker runs here and the console that underlines the
			// position is on the other side (CLAUDE.md rule 11).
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
// database refused this query or this service failed. Only a *pgconn.PgError
// is the database's; anything else is internal, so our infrastructure is never
// shown to a participant as a mistake in their SQL. The message travels either
// way for the journal; the contract says it is never shown to the participant.
//
// The order matters: pgx nests a refused login's PgError (28P01) inside a
// *pgconn.ConnectError that carries the cluster's address and role. Checking
// for a PgError first would show those to the participant. A connection
// failure is internal: the database never saw the query.
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

// wirePosition narrows a parser's offset to the int32 field. A bare
// conversion would wrap past 2^31 into a negative editor position; relying on
// sqlpolicy.MaxQueryBytes would tie this line to another package's constant.
// Anything out of range becomes 0, "no position".
func wirePosition(position int) int32 {
	if position <= 0 || position > math.MaxInt32 {
		return 0
	}
	return int32(position)
}

// errorFor turns the wire's answer back into the queryrunner package's own
// errors, so a caller behaves the same against a local or remote runner,
// including errors.Is and the refusal code the interface translates.
func errorFor(failure *pb.Failure) error {
	if failure == nil {
		return nil
	}

	switch failure.GetKind() {
	case pb.Failure_KIND_REFUSED:
		return &sqlpolicy.Refusal{
			Code:    sqlpolicy.Code(failure.GetCode()),
			Subject: failure.GetSubject(),
			// 1-based; zero means "no position".
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
		return &queryrunner.DatabaseError{Message: failure.GetMessage()}
	case pb.Failure_KIND_INTERNAL:
		// ErrUnreachable: the Core API answers it with a generic, retryable
		// 503, right for a runner that could not reach the game cluster.
		// Importing queryproxy's sentinel would invert the dependency. The
		// message is for the log only; %s because only text is left.
		return fmt.Errorf("%w: %s", ErrUnreachable, failure.GetMessage())
	default:
		// An unknown kind from a newer runner is still a failure.
		return errors.New("the query service refused the query: " + failure.GetMessage())
	}
}

// policyProto and policyFrom carry the contest's policy across. Mode stays a
// string: a wire enum would be a second list to keep in step with sqlpolicy's.
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
