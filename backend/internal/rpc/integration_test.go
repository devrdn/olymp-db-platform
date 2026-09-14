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

// The whole path, assembled: a client, a socket, a server, a runner and a real
// database. It exists because everything either side of the wire is tested
// separately and none of that says the two halves agree — and because the
// point of the split is that a caller cannot tell the difference, which is a
// claim only an end-to-end test can make.

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
	// NULL has to arrive as nothing rather than as an empty string: a
	// participant debugging a left join is looking at exactly that column.
	if result.Rows[1][1] != nil {
		t.Fatalf("a NULL arrived as %#v", result.Rows[1][1])
	}
}

// The refusal has to arrive as a refusal, with its code, because that is what
// the interface turns into a sentence in the participant's language.
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

// The deadline is the reason this service exists at all, so it has to be the
// same deadline when reached through the wire.
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

// The journal wraps an executor, and after the split the executor is this
// client. If that stopped compiling the split would have cost the query log,
// which is the one thing the Core API keeps on its own side of the wire.
func TestTheJournalCanWrapTheClient(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	var executor queryrunner.Executor = client
	if executor == nil {
		t.Fatal("the client is not an executor")
	}
}

// The container's health check is this binary dialling itself, because the
// runtime image has neither a shell nor a gRPC probe. If the health service
// stopped being registered, every deployment would report a runner that never
// becomes healthy — and would do so only in production.
func TestTheServiceReportsItselfHealthy(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	if err := Probe(t.Context(), client.conn.Target(), theToken); err != nil {
		t.Fatalf("the service is not reporting itself healthy: %v", err)
	}
}

func TestAListenAddressBecomesOneThatCanBeDialled(t *testing.T) {
	// ":9100" means every interface when listening and nothing at all when
	// connecting, which is the whole reason this exists.
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

// A result the runner considers acceptable must be one the transport can
// carry. It was not: gRPC's default receive limit is 4 MiB and the runner's
// own budget is 5 MiB, so an answer between the two came back as
// ResourceExhausted — which the client reported as the service being unable to
// answer and the journal recorded as an error. A big answer looked like an
// outage.
func TestAnAnswerInsideTheBudgetArrivesWhole(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	// Five million bytes: comfortably past gRPC's default, comfortably inside
	// the five mebibyte budget. Precisely the range that used to fail.
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

// Past the budget it is cut, not refused — and cut by what is actually sent.
// The runner's own count is over the Go values it read, and a value counted as
// eight bytes can render as twenty characters, so that count is a floor rather
// than a bound. This is the layer that knows the real size.
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

// The disk quota is decided on the Core API's side and checked on the runner's,
// so it has to cross the wire. It did not: the request carried no such field,
// and the check that section 4.1 puts first was dead in the one arrangement
// the deployment actually uses.
func TestTheDiskQuotaCrossesTheWire(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := ask(database, `INSERT INTO evidence (id, note) VALUES (9, 'planted')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	writing.DiskQuotaBytes = 1

	if _, err := client.Run(t.Context(), writing); !errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatalf("error = %v, want ErrDiskFull", err)
	}
}

// What a write answers with is a count, and the count has to arrive.
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

// An answer too large to read at all is its own kind of failure on the wire,
// so that the caller can say so rather than reporting the database as broken.
func TestAnAnswerTooLargeToReadArrivesAsSuch(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = 64 << 10
	client, database := serving(t, limits, checker.NewChecker())

	_, err := client.Run(t.Context(), ask(database, `SELECT string_agg(repeat('x', 10000), '') FROM generate_series(1, 1000)`))
	if !errors.Is(err, queryrunner.ErrResultTooLarge) {
		t.Fatalf("error = %v, want ErrResultTooLarge", err)
	}
}

// A separate service whose logs cannot be joined to the requests that caused
// them is a separate service nobody can debug. The identifier travels as
// metadata, so every line the runner writes about a call carries the same
// request_id as the line the Core API wrote about it.
func TestTheRequestIdentifierCrossesIntoTheOtherProcess(t *testing.T) {
	client, database := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	const id = "9d1f0c3a-known"
	ctx := logging.WithRequestID(t.Context(), id)

	// The server puts it into its own context, where the logger picks it up.
	// Asserting on what the runner logs would mean logging per query, which it
	// deliberately does not; asserting the context carried it is the same
	// fact one step earlier.
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

// CLAUDE.md rule 11: a value decided on one side of this contract and shown on
// the other has to cross it. The runner resolves a column's type from the row
// description and times the statement it ran; both are useless unless the
// Core API — which never holds the connection that knew either — receives
// them. The deployment has this service in a separate process, so this is the
// only arrangement in which the console's column types and its meter exist at
// all.
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

// The duration crosses as a number and a unit, and only one of the two is
// written down. A thousandfold error in either direction still arrives as a
// plausible-looking duration, so this pins the magnitude against something
// the test measured itself.
//
// Counting ten million rows server-side is tens of milliseconds and cannot be
// less than one; the whole call is the ceiling, because the answer was carried
// by it. Nanoseconds mistaken for microseconds put the value a thousand times
// over that ceiling, and microseconds mistaken for nanoseconds put it a
// thousand times under the floor.
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

// A write answers with a count and no columns at all. Nothing may invent a
// list for it — an interface drawing a header from an empty answer draws an
// empty header.
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

// The whole path, for the failure that started this: the runner cannot open a
// connection, and what the Core API is handed must be ours rather than the
// database's own words about the query.
//
// Through the real transport (CLAUDE.md rule 10), because that is the only
// arrangement a deployment uses and because the classification is made on one
// side of the contract and acted on at the other. A database that does not
// exist fails the connection the same way a wrong password does — PostgreSQL
// answers FATAL, so the error carries a *pgconn.PgError inside a connection
// failure, which is exactly the shape that used to be forwarded to a
// participant as "your query was wrong".
func TestAConnectionTheRunnerCouldNotOpenArrivesAsOursNotTheDatabases(t *testing.T) {
	client, _ := serving(t, queryrunner.DefaultLimits(), checker.NewChecker())

	// A plain identifier, so the runner tries to connect rather than refusing
	// the name — and no database of that name exists on the cluster.
	_, err := client.Run(t.Context(), ask("game_no_such_database_at_all", `SELECT 1`))

	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want it to be ErrUnreachable", err)
	}
	var database *queryrunner.DatabaseError
	if errors.As(err, &database) {
		t.Fatalf("a connection the runner never opened arrived as the database's own words: %v", database)
	}
}

// The console underlines the character PostgreSQL objected to, and that
// character is decided inside the Query Runner — the checker runs there,
// because it links PostgreSQL's parser through cgo. Without a field on the
// contract the number is produced on one side of the wire and read on the
// other, so it is zero in every arrangement a deployment actually uses: the
// console only exists when QUERY_RUNNER_ADDR is set (CLAUDE.md rule 11).
//
// Cyrillic on purpose. The offset is a 1-based *character* count, and in UTF-8
// these are two bytes each — so a byte offset and a character offset differ by
// a factor of two here, and a wire that quietly carried the wrong one would
// still look right in every ASCII test.
func TestAParseErrorsPositionSurvivesTheWireForACyrillicQuery(t *testing.T) {
	local := checker.NewChecker()
	client, database := serving(t, queryrunner.DefaultLimits(), local)

	// A comment in Cyrillic, then a statement the parser cannot finish. The
	// text before the mistake is all multi-byte, so any byte/character
	// confusion shows up as a position roughly twice what it should be.
	const sql = "-- отчёт по гостям\nSELECT FROM"

	// What the checker itself says, asked here rather than hard-coded: the
	// claim is that the wire carries *the checker's own* number, not that
	// somebody counted the characters correctly in a test.
	direct := local.Check(sql, sqlpolicy.ReadOnly())
	var expected *sqlpolicy.Refusal
	if !errors.As(direct, &expected) || expected.Code != sqlpolicy.CodeParseError {
		t.Fatalf("the checker answered %v, want a parse error to carry a position", direct)
	}
	if expected.Position <= 0 {
		t.Fatalf("the checker located the error at %d; there is nothing to carry", expected.Position)
	}
	// And it really is a character offset into a string whose bytes outnumber
	// its characters, or this test proves nothing about the distinction. The
	// ceiling is one past the last character, which is where PostgreSQL points
	// at a statement that ended too early; a byte offset into this query would
	// be half as far again.
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
