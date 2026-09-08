package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
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
		// PostgreSQL's own error, which is what a database error is. A bare
		// errors.New here would be a test of the default branch wearing the
		// name of this one.
		"a database error": {&pgconn.PgError{
			Severity: "ERROR", Code: "42P01", Message: `relation "nope" does not exist`,
		}, nil},
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

// The position is the other half of what makes a refusal actionable, and it
// was the half with no field on the contract: the checker set it, the console
// read it, and in every deployed arrangement it was zero between the two.
//
// Both directions and both values, because "no position" is a real answer —
// every refusal but a parse error has one, and a wire that invented an offset
// for them would have the console underline the first character of a query
// whose whole statement was refused.
func TestARefusalKeepsThePositionItWasRefusedAt(t *testing.T) {
	for name, given := range map[string]struct {
		refusal *sqlpolicy.Refusal
		want    int
	}{
		"a parse error, at the character the parser stopped on": {
			&sqlpolicy.Refusal{Code: sqlpolicy.CodeParseError, Subject: "syntax error", Position: 15}, 15,
		},
		"a refusal about a whole statement, with nowhere to point": {
			&sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: "pg_sleep"}, 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var back *sqlpolicy.Refusal
			if !errors.As(errorFor(failureFor(given.refusal)), &back) {
				t.Fatal("a refusal came back as something else")
			}
			if back.Position != given.want {
				t.Fatalf("position came back as %d, want %d", back.Position, given.want)
			}
		})
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

// A connection that never opened is not the database refusing anything: the
// database never saw the query.
//
// This is the failure that was actually served to a participant — the Query
// Runner could not reach the game cluster, and the classification here called
// it a database error, so the console answered "check the fields you filled
// in" with the cluster's host, port, role name and the participant's own
// database name attached.
//
// The refusal is provoked rather than described, because the shape of the
// error is the whole point: pgx reports a rejected login as a
// *pgconn.ConnectError whose chain *contains* a *pgconn.PgError, so a rule
// that only asks "is there a PgError in here" answers yes for a connection
// failure and hands the connection string back as though PostgreSQL had said
// it about the query.
func TestAConnectionThatNeverOpenedIsNotTheDatabaseRefusingTheQuery(t *testing.T) {
	err := fmt.Errorf("connecting to the game database: %w", refusedLogin(t))

	failure := failureFor(err)
	if got := failure.GetKind(); got != pb.Failure_KIND_INTERNAL {
		t.Fatalf("kind = %v, want KIND_INTERNAL", got)
	}

	// The journal still gets the whole thing: it is the only place the
	// address and the role that failed are of any use.
	if !strings.Contains(failure.GetMessage(), "game_reader") {
		t.Fatalf("the message the journal reads lost the detail: %q", failure.GetMessage())
	}

	back := errorFor(failure)
	if !errors.Is(back, ErrUnreachable) {
		t.Fatalf("came back as %v, want it to be ErrUnreachable", back)
	}
	var database *queryrunner.DatabaseError
	if errors.As(back, &database) {
		t.Fatalf("a connection failure came back as the database's own words: %v", database)
	}
}

// PostgreSQL refusing the query is the one thing here that is about the
// query, and its words are the useful ones — "relation \"guests\" does not
// exist" is the sentence a participant can act on.
func TestTheDatabasesOwnRefusalCrossesAsTheDatabasesOwn(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: `relation "guests" does not exist`}

	failure := failureFor(fmt.Errorf("running the statement: %w", pgErr))
	if got := failure.GetKind(); got != pb.Failure_KIND_DATABASE_ERROR {
		t.Fatalf("kind = %v, want KIND_DATABASE_ERROR", got)
	}

	var database *queryrunner.DatabaseError
	if !errors.As(errorFor(failure), &database) {
		t.Fatalf("the database's own error came back as something else: %v", errorFor(failure))
	}
	if !strings.Contains(database.Error(), "guests") {
		t.Fatalf("the database's own words were lost: %q", database.Error())
	}
}

// Anything unrecognised is ours until it proves otherwise. A bug of ours must
// not be reported to a participant as the database's answer to their SQL.
func TestAnUnrecognisedFailureIsOursAndNotTheDatabases(t *testing.T) {
	failure := failureFor(errors.New("the runner is holding it wrong"))
	if got := failure.GetKind(); got != pb.Failure_KIND_INTERNAL {
		t.Fatalf("kind = %v, want KIND_INTERNAL", got)
	}

	back := errorFor(failure)
	if !errors.Is(back, ErrUnreachable) {
		t.Fatalf("came back as %v, want it to be ErrUnreachable", back)
	}
	var database *queryrunner.DatabaseError
	if errors.As(back, &database) {
		t.Fatalf("a failure of ours came back as the database's own words: %v", database)
	}
}

// refusedLogin returns the error pgx produces when a cluster refuses the
// login — the second half of what the participant was shown.
//
// A listener that speaks the two messages of the protocol this needs, rather
// than a real cluster: the error's *shape* is what is under test, and a test
// that skips without a database is a test that does not run on the machine
// where somebody breaks this.
func refusedLogin(t *testing.T) error {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		backend := pgproto3.NewBackend(conn, conn)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			return
		}
		message, err := (&pgproto3.ErrorResponse{
			Severity: "FATAL", Code: "28P01",
			Message: `password authentication failed for user "game_reader"`,
		}).Encode(nil)
		if err != nil {
			return
		}
		_, _ = conn.Write(message)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, "postgres://game_reader:wrong@"+listener.Addr().String()+
		"/game_pool_ce678159661a1_57c5ba38100c?sslmode=disable")
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatal("the listener accepted a login it was written to refuse")
	}
	return err
}
