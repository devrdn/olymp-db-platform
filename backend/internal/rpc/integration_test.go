package rpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

// The whole path: client, socket, server, runner and a real database. Only an
// end-to-end test shows the two halves agree (CLAUDE.md rule 10).

func serving(t *testing.T, limits queryrunner.Limits, checker *checker.Checker) (*Client, string) {
	t.Helper()

	database := gamedbtest.Scratch(t)
	gamedbtest.Run(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY, note text)`,
		`INSERT INTO evidence VALUES (1, 'a knife'), (2, NULL)`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter,
		`GRANT INSERT ON evidence TO `+gamedb.RoleWriter,
	)

	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword(t), database),
		gamedbtest.DSN(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), database))
	if err != nil {
		t.Fatalf("building the cluster connector: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, lis, NewServer(queryrunner.New(cluster, checker, limits), limits, quiet), theToken, 5*time.Second, quiet)
	}()

	client, err := Dial(lis.Addr().String(), theToken)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
		<-done
	})

	return client, database
}

func ask(database, sql string) queryrunner.Request {
	return queryrunner.Request{
		Registration: uuid.New(),
		Database:     database,
		SQL:          sql,
		Policy:       sqlpolicy.ReadOnly(),
	}
}

func TestAQueryAndItsAnswerCrossTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	result, err := client.Run(t.Context(), ask(database, `SELECT id, note FROM evidence ORDER BY id`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if len(result.Columns) != 2 || result.Columns[0] != "id" {
		t.Fatalf("columns = %v", result.Columns)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}
	if result.Rows[0][1] != "a knife" {
		t.Fatalf("first note = %v", result.Rows[0][1])
	}
	// NULL arrives as nil, not as an empty string.
	if result.Rows[1][1] != nil {
		t.Fatalf("a NULL arrived as %#v", result.Rows[1][1])
	}
	// Rows share one backing array; growing one must not write into the next.
	_ = append(result.Rows[0], "extra")
	if result.Rows[1][0] != "2" {
		t.Fatalf("second id = %#v after the first row grew, want \"2\"", result.Rows[1][0])
	}
}

func TestARefusalArrivesAsARefusal(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	_, err := client.Run(t.Context(), ask(database, `SELECT pg_sleep(1)`))

	var refusal *sqlpolicy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refusal.Code != sqlpolicy.CodeFunctionNotSupported || refusal.Subject != "pg_sleep" {
		t.Fatalf("refusal = %+v", refusal)
	}
}

func TestTruncationSurvivesTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	result, err := client.Run(t.Context(), ask(database, `SELECT g FROM generate_series(1, 5000) g`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if !result.Truncated {
		t.Fatal("a cut result arrived saying it was complete")
	}
	if len(result.Rows) != queryrunner.DefaultLimits().MaxRows {
		t.Fatalf("rows = %d", len(result.Rows))
	}
}

func TestTheDeadlineStillBoundsTimeThroughTheWire(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 400 * time.Millisecond
	client, database := serving(t, limits, checker.NewChecker("pg_sleep"))

	started := time.Now()
	_, err := client.Run(t.Context(), ask(database, `SELECT pg_sleep(30)`))
	elapsed := time.Since(started)

	if !errors.Is(err, queryrunner.ErrTimeout) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took %s — something other than the runner bounded it", elapsed)
	}
}

func TestAFullInstanceSaysSoRatherThanWaiting(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Concurrent = 1
	limits.QueueDepth = 0
	limits.Deadline = 3 * time.Second
	client, database := serving(t, limits, checker.NewChecker("pg_sleep"))

	holding := make(chan struct{})
	go func() {
		close(holding)
		_, _ = client.Run(context.Background(), ask(database, `SELECT pg_sleep(1)`))
	}()
	<-holding
	time.Sleep(300 * time.Millisecond)

	if _, err := client.Run(t.Context(), ask(database, `SELECT 1`)); !errors.Is(err, queryrunner.ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
}

// The journal wraps an executor, and the executor is this client.
func TestTheJournalCanWrapTheClient(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	var executor queryrunner.Executor = client
	if executor == nil {
		t.Fatal("the client is not an executor")
	}
}

// The container's health check is this binary dialling itself; without the
// health service a deployed runner never becomes healthy.
func TestTheServiceReportsItselfHealthy(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	if err := Probe(t.Context(), client.conn.Target(), theToken); err != nil {
		t.Fatalf("the service is not reporting itself healthy: %v", err)
	}
}

func TestAListenAddressBecomesOneThatCanBeDialled(t *testing.T) {
	if got := ProbeAddress(":9100"); got != "127.0.0.1:9100" {
		t.Fatalf("ProbeAddress(\":9100\") = %q", got)
	}
	if got := ProbeAddress("runner:9100"); got != "runner:9100" {
		t.Fatalf("a full address was rewritten to %q", got)
	}
	if got := ProbeAddress(""); got != "127.0.0.1:9100" {
		t.Fatalf("an empty address became %q", got)
	}
}

// gRPC's default 4 MiB receive limit is below the runner's 5 MiB budget; an
// answer between the two must still arrive.
func TestAnAnswerInsideTheBudgetArrivesWhole(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	// Five million bytes: past gRPC's default, inside the 5 MiB budget.
	result, err := client.Run(t.Context(),
		ask(database, `SELECT repeat('x', 5000) FROM generate_series(1, 1000)`))
	if err != nil {
		t.Fatalf("an answer inside the budget failed to arrive: %v", err)
	}
	if result.Truncated {
		t.Fatal("an answer inside the budget was cut")
	}
	if len(result.Rows) != 1000 {
		t.Fatalf("rows = %d, want 1000", len(result.Rows))
	}
}

// Past the budget an answer is cut by its rendered size, not refused.
func TestAnAnswerBeyondTheBudgetIsCutRatherThanRefused(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	result, err := client.Run(t.Context(),
		ask(database, `SELECT repeat('x', 6000) FROM generate_series(1, 1000)`))
	if err != nil {
		t.Fatalf("an oversized answer failed instead of being cut: %v", err)
	}
	if !result.Truncated {
		t.Fatal("an answer over the byte budget arrived saying it was complete")
	}
	if len(result.Rows) == 0 || len(result.Rows) >= 1000 {
		t.Fatalf("rows = %d; the budget cut nothing useful", len(result.Rows))
	}
}

// The disk quota is decided by the Core API and checked by the runner, so it
// must cross the wire (CLAUDE.md rule 11).
func TestTheDiskQuotaCrossesTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := ask(database, `INSERT INTO evidence (id, note) VALUES (9, 'planted')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	writing.DiskQuotaBytes = 1

	if _, err := client.Run(t.Context(), writing); !errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatalf("error = %v, want ErrDiskFull", err)
	}
}

func TestRowsAffectedCrossTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := ask(database, `INSERT INTO evidence (id, note) VALUES (7, 'a hat'), (8, 'a cane')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	result, err := client.Run(t.Context(), writing)
	if err != nil {
		t.Fatalf("a permitted write failed across the wire: %v", err)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("rows affected = %d, want 2", result.RowsAffected)
	}
}

func TestAnAnswerTooLargeToReadArrivesAsSuch(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = 64 << 10
	client, database := serving(t, limits, checker.NewChecker())

	_, err := client.Run(t.Context(), ask(database, `SELECT string_agg(repeat('x', 10000), '') FROM generate_series(1, 1000)`))
	if !errors.Is(err, queryrunner.ErrResultTooLarge) {
		t.Fatalf("error = %v, want ErrResultTooLarge", err)
	}
}

func TestTheRequestIdentifierCrossesIntoTheOtherProcess(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	const id = "9d1f0c3a-known"
	ctx := logging.WithRequestID(t.Context(), id)

	// The runner logs nothing per query, so the test asserts on the context.
	var seen string
	carried = func(ctx context.Context) { seen = logging.RequestIDFrom(ctx) }
	t.Cleanup(func() { carried = nil })

	if _, err := client.Run(ctx, ask(database, `SELECT 1`)); err != nil {
		t.Fatalf("running: %v", err)
	}
	if seen != id {
		t.Fatalf("the runner saw request id %q, want %q", seen, id)
	}
}

// The runner resolves column types and times the statement; the Core API
// shows them (CLAUDE.md rule 11).
func TestTheColumnTypesAndTheDurationCrossTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	result, err := client.Run(t.Context(), ask(database,
		`SELECT 'Margot'::text AS full_name, now() AS at, id FROM evidence ORDER BY id LIMIT 1`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	want := []string{"text", "timestamp with time zone", "integer"}
	if len(result.ColumnTypes) != len(want) {
		t.Fatalf("column types = %v, want %d of them", result.ColumnTypes, len(want))
	}
	for i := range want {
		if result.ColumnTypes[i] != want[i] {
			t.Fatalf("type of %q arrived as %q, want %q",
				result.Columns[i], result.ColumnTypes[i], want[i])
		}
	}

	if result.Duration <= 0 {
		t.Fatalf("duration = %v; the statement's own time did not cross the wire", result.Duration)
	}
}

// A thousandfold unit error still looks plausible, so the duration is pinned
// between a floor (ten million rows take over a millisecond) and the whole
// call's measured time.
func TestTheDurationKeepsItsUnitAcrossTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	before := time.Now()
	result, err := client.Run(t.Context(), ask(database, `SELECT count(*) FROM generate_series(1, 100000) a, generate_series(1, 100) b`))
	whole := time.Since(before)
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if result.Duration < time.Millisecond {
		t.Fatalf("counting ten million rows was reported as %v; the duration arrived "+
			"in a smaller unit than the contract's microseconds", result.Duration)
	}
	if result.Duration > whole {
		t.Fatalf("the statement was reported as %v of a call that took %v in total; "+
			"the duration arrived in a larger unit than the contract's microseconds",
			result.Duration, whole)
	}
}

// A write has no columns; nothing may invent a type list for it.
func TestAWriteCrossesTheWireWithNoColumnTypes(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := ask(database, `INSERT INTO evidence VALUES (3, 'a torn ticket')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	result, err := client.Run(t.Context(), writing)
	if err != nil {
		t.Fatalf("running the insert: %v", err)
	}
	if len(result.ColumnTypes) != 0 {
		t.Fatalf("a write with no columns arrived with types %v", result.ColumnTypes)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("rows affected = %d, want 1", result.RowsAffected)
	}
	if result.Duration <= 0 {
		t.Fatalf("duration = %v; a write's own time did not cross the wire", result.Duration)
	}
}

// Classified on one side of the contract and acted on at the other, so tested
// over the real transport (CLAUDE.md rule 10). A missing database fails like a
// wrong password: a *pgconn.PgError inside a connection failure.
func TestAConnectionTheRunnerCouldNotOpenArrivesAsOursNotTheDatabases(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	// A plain identifier, so the runner tries to connect rather than refusing it.
	_, err := client.Run(t.Context(), ask("game_no_such_database_at_all", `SELECT 1`))

	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want it to be ErrUnreachable", err)
	}
	var database *queryrunner.DatabaseError
	if errors.As(err, &database) {
		t.Fatalf("a connection the runner never opened arrived as the database's own words: %v", database)
	}
}

// The checker runs in the Query Runner and the console underlines on the other
// side (CLAUDE.md rule 11). Cyrillic because the offset is a 1-based character
// count: two-byte characters make a byte offset distinguishable from it.
func TestAParseErrorsPositionSurvivesTheWireForACyrillicQuery(t *testing.T) {
	local := checker.NewChecker()
	client, database := serving(t, queryrunner.DefaultLimits(), local)

	const sql = "-- отчёт по гостям\nSELECT FROM"

	// Asked of the checker rather than hard-coded: the wire must carry its number.
	direct := local.Check(sql, sqlpolicy.ReadOnly())
	var expected *sqlpolicy.Refusal
	if !errors.As(direct, &expected) || expected.Code != sqlpolicy.CodeParseError {
		t.Fatalf("the checker answered %v, want a parse error to carry a position", direct)
	}
	if expected.Position <= 0 {
		t.Fatalf("the checker located the error at %d; there is nothing to carry", expected.Position)
	}
	// One past the last character is where PostgreSQL points at a statement
	// that ended too early; a byte offset would be further.
	characters := utf8.RuneCountInString(sql)
	if len(sql) <= characters+1 {
		t.Fatal("the query has too little Cyrillic in it; a byte offset and a character offset would be indistinguishable")
	}
	if expected.Position > characters+1 {
		t.Fatalf("position %d is past the %d characters of the query — that is a byte offset",
			expected.Position, characters)
	}

	_, err := client.Run(t.Context(), ask(database, sql))

	var refusal *sqlpolicy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refusal.Position != expected.Position {
		t.Fatalf("the position arrived as %d, want the checker's own %d", refusal.Position, expected.Position)
	}
}
