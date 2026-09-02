package rpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// The whole path, assembled: a client, a socket, a server, a runner and a real
// database. It exists because everything either side of the wire is tested
// separately and none of that says the two halves agree — and because the
// point of the split is that a caller cannot tell the difference, which is a
// claim only an end-to-end test can make.

func serving(t *testing.T, limits queryrunner.Limits, checker *sqlpolicy.Checker) (*Client, string) {
	t.Helper()

	database := gamedbtest.Scratch(t)
	gamedbtest.Run(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY, note text)`,
		`INSERT INTO evidence VALUES (1, 'a knife'), (2, NULL)`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader,
	)

	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword, database))
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
		_ = Serve(ctx, lis, NewServer(queryrunner.New(cluster, checker, limits), limits, quiet), 5*time.Second, quiet)
	}()

	client, err := Dial(lis.Addr().String())
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
	client, database := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
	client, database := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
	client, database := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
	client, database := serving(t, limits, sqlpolicy.NewChecker("pg_sleep"))

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
	client, database := serving(t, limits, sqlpolicy.NewChecker("pg_sleep"))

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
	client, _ := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
	client, _ := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

	if err := Probe(t.Context(), client.conn.Target()); err != nil {
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
	client, database := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
	client, database := serving(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())

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
