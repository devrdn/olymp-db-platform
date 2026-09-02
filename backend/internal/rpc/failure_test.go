package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// Every failure the runner can produce has to survive the wire as the same
// failure. The two directions are separate code, and the enum is a third list
// again — this is what keeps the three in step, and it is the same class of
// mistake as a Go status the query_log column would have rejected.
func TestEveryFailureSurvivesTheWireAsItself(t *testing.T) {
	for name, given := range map[string]struct {
		err  error
		want error
	}{
		"a refusal":       {&sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: "pg_sleep"}, nil},
		"a full instance": {queryrunner.ErrBusy, queryrunner.ErrBusy},
		"one at a time":   {queryrunner.ErrAlreadyRunning, queryrunner.ErrAlreadyRunning},
		"a timeout":       {queryrunner.ErrTimeout, queryrunner.ErrTimeout},
		"a deadline":      {context.DeadlineExceeded, queryrunner.ErrTimeout},
		// A caller that left is not a query that ran too long. Conflating them
		// here would put back the difference the runner just took out.
		"a caller that left":  {queryrunner.ErrCanceled, queryrunner.ErrCanceled},
		"a cancelled context": {context.Canceled, queryrunner.ErrCanceled},
		"asking too fast":     {queryrunner.ErrTooManyQueries, queryrunner.ErrTooManyQueries},
		"a full disk":         {queryrunner.ErrDiskFull, queryrunner.ErrDiskFull},
		"a database error":    {errors.New("relation \"nope\" does not exist"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			back := errorFor(failureFor(given.err))
			if back == nil {
				t.Fatal("a failure came back as success")
			}
			if given.want != nil && !errors.Is(back, given.want) {
				t.Fatalf("came back as %v, want %v", back, given.want)
			}
		})
	}
}

// A refusal is the one failure the interface has to act on rather than merely
// report: the code chooses which sentence the participant is shown, in their
// own language. Losing it across the wire would leave the interface with an
// English string and nothing to translate.
func TestARefusalKeepsItsCodeAndSubject(t *testing.T) {
	original := &sqlpolicy.Refusal{Code: sqlpolicy.CodeCatalogNotReadable, Subject: "pg_stat_activity"}

	var back *sqlpolicy.Refusal
	if !errors.As(errorFor(failureFor(original)), &back) {
		t.Fatal("a refusal came back as something else")
	}
	if back.Code != original.Code || back.Subject != original.Subject {
		t.Fatalf("came back as %+v, want %+v", back, original)
	}
}

func TestSuccessCarriesNoFailure(t *testing.T) {
	if failureFor(nil) != nil {
		t.Fatal("success produced a failure")
	}
	if errorFor(nil) != nil {
		t.Fatal("no failure produced an error")
	}
}

// A kind this build has never heard of still means the query did not run.
// Treating an unfamiliar enum as success would turn a newer runner into
// silently wrong answers rather than a visible incompatibility.
func TestAnUnknownKindIsStillAFailure(t *testing.T) {
	if err := errorFor(&pb.Failure{Kind: pb.Failure_Kind(99).Enum(), Message: ptr("from the future")}); err == nil {
		t.Fatal("an unknown failure kind came back as success")
	}
}

// The policy decides what a participant may do, and it crosses the wire on
// every request. A field that fails to travel is a contest running under a
// policy nobody chose — most dangerously the writable tables, where the
// difference between "empty" and "lost" is invisible at the far end.
func TestThePolicyCrossesWholeInBothDirections(t *testing.T) {
	original := sqlpolicy.Policy{
		Mode:            sqlpolicy.ModeReadWrite,
		WritableTables:  []string{"evidence", "case_notes"},
		AllowCreateView: true,
		AllowOwnTables:  true,
		AllowTempTables: true,
		AllowCatalog:    true,
	}

	back := policyFrom(policyProto(original))

	if back.Mode != original.Mode {
		t.Fatalf("mode = %q, want %q", back.Mode, original.Mode)
	}
	if len(back.WritableTables) != 2 || back.WritableTables[0] != "evidence" {
		t.Fatalf("writable tables = %v", back.WritableTables)
	}
	if !back.AllowCreateView || !back.AllowOwnTables || !back.AllowTempTables || !back.AllowCatalog {
		t.Fatalf("a permission was lost: %+v", back)
	}

	// And the restrictive one stays restrictive, which is the direction that
	// matters if a field ever stops travelling.
	strict := policyFrom(policyProto(sqlpolicy.ReadOnly()))
	if strict.Mode != sqlpolicy.ModeReadOnly || len(strict.WritableTables) != 0 {
		t.Fatalf("read-only did not survive: %+v", strict)
	}
	if err := strict.Validate(); err != nil {
		t.Fatalf("a policy that crossed the wire no longer validates: %v", err)
	}
}

// A timeout and a cancellation must not collapse into each other on the way
// across, in either direction.
func TestATimeoutAndACancellationStayDistinct(t *testing.T) {
	timeout := errorFor(failureFor(queryrunner.ErrTimeout))
	cancelled := errorFor(failureFor(queryrunner.ErrCanceled))

	if errors.Is(timeout, queryrunner.ErrCanceled) {
		t.Fatal("a timeout arrived as a cancellation")
	}
	if errors.Is(cancelled, queryrunner.ErrTimeout) {
		t.Fatal("a cancellation arrived as a timeout")
	}
}
