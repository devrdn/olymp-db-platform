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

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
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
		return &pb.Failure{Kind: pb.Failure_KIND_DATABASE_ERROR.Enum(), Message: ptr(err.Error())}
	}
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
		return errors.New(failure.GetMessage())
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
