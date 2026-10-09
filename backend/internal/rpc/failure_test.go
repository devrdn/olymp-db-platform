package rpc

import (
	"context"
	"errors"
	"fmt"
	"math"
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

// Keeps the two mapping directions and the wire enum in step.
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
		// A caller that left is not a query that ran too long.
		"a caller that left":  {queryrunner.ErrCanceled, queryrunner.ErrCanceled},
		"a cancelled context": {context.Canceled, queryrunner.ErrCanceled},
		"asking too fast":     {queryrunner.ErrTooManyQueries, queryrunner.ErrTooManyQueries},
		"a full disk":         {queryrunner.ErrDiskFull, queryrunner.ErrDiskFull},
		// A bare errors.New would exercise the default branch instead.
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

// The code picks the translated sentence the participant is shown.
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

// Zero is a real answer too: an invented offset would underline the first
// character of a query refused as a whole (CLAUDE.md rule 11).
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

func TestAnUnknownKindIsStillAFailure(t *testing.T) {
	if err := errorFor(&pb.Failure{Kind: pb.Failure_Kind(99).Enum(), Message: ptr("from the future")}); err == nil {
		t.Fatal("an unknown failure kind came back as success")
	}
}

// A field that fails to travel is a contest running under a policy nobody
// chose; for writable tables, "empty" and "lost" look the same.
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

	// The restrictive policy must stay restrictive.
	strict := policyFrom(policyProto(sqlpolicy.ReadOnly()))
	if strict.Mode != sqlpolicy.ModeReadOnly || len(strict.WritableTables) != 0 {
		t.Fatalf("read-only did not survive: %+v", strict)
	}
	if err := strict.Validate(); err != nil {
		t.Fatalf("a policy that crossed the wire no longer validates: %v", err)
	}
}

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

// The refusal is provoked rather than built by hand, because the error's
// shape is the point: pgx wraps the login's PgError in a ConnectError that
// carries the cluster's address and role.
func TestAConnectionThatNeverOpenedIsNotTheDatabaseRefusingTheQuery(t *testing.T) {
	err := fmt.Errorf("connecting to the game database: %w", refusedLogin(t))

	failure := failureFor(err)
	if got := failure.GetKind(); got != pb.Failure_KIND_INTERNAL {
		t.Fatalf("kind = %v, want KIND_INTERNAL", got)
	}

	// The journal still gets the full detail.
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
// login. A fake listener rather than a real cluster, so the test never skips.
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

func TestAPositionTooLargeForTheWireArrivesAsNoPositionAtAll(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int
		want int32
	}{
		{"an ordinary offset", 31, 31},
		{"no position at all", 0, 0},
		{"a negative offset nothing should have produced", -5, 0},
		{"one past what the field can hold", math.MaxInt32 + 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wirePosition(tc.in); got != tc.want {
				t.Fatalf("wirePosition(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
