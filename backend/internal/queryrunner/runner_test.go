package queryrunner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

func TestItReturnsColumnsAndRows(t *testing.T) {
	runner, database := setup(t)

	result, err := runner.Run(t.Context(), request(database, `SELECT id, note FROM evidence ORDER BY id`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if got := result.Columns; len(got) != 2 || got[0] != "id" || got[1] != "note" {
		t.Fatalf("columns = %v", got)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}
	if result.Truncated {
		t.Fatal("a two-row result was reported as truncated")
	}
}

// The wrapper that limits a result is string surgery on the participant's own
// SQL, so the shapes that break string surgery are the ones worth pinning.
func TestTheShapesThatBreakAWrapper(t *testing.T) {
	runner, database := setup(t)

	for name, sql := range map[string]string{
		// The wrapper's closing bracket has to be on its own line, or a
		// trailing line comment swallows it and nothing parses.
		"a trailing line comment": "SELECT 1 AS a -- what I was thinking",
		"a trailing semicolon":    `SELECT 1 AS a;`,
		"trailing whitespace":     "SELECT 1 AS a   \n\t ",
		// Two columns of the same name are legal inside a subquery; a wrapper
		// that assumed otherwise would refuse an ordinary join.
		"two columns named alike": `SELECT a.x, b.x FROM (SELECT 1 x) a, (SELECT 2 x) b`,
		"a LIMIT of its own":      `SELECT id FROM evidence LIMIT 1`,
		"an ORDER BY of its own":  `SELECT id FROM evidence ORDER BY id DESC`,
		"a bare VALUES":           `VALUES (1), (2)`,
		"a CTE":                   `WITH x AS (SELECT 1 AS a) SELECT * FROM x`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runner.Run(t.Context(), request(database, sql)); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		})
	}
}

// EXPLAIN cannot go inside a subquery, so it is the one shape the wrapper must
// leave alone. A runner that wrapped it would refuse the only query a
// participant has for understanding why theirs is slow.
func TestExplainIsNotWrapped(t *testing.T) {
	runner, database := setup(t)

	result, err := runner.Run(t.Context(), request(database, `EXPLAIN SELECT * FROM evidence`))
	if err != nil {
		t.Fatalf("running EXPLAIN: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatal("EXPLAIN returned no plan")
	}
}

func TestALongResultIsCutAndSaysSo(t *testing.T) {
	runner, database := setup(t)

	result, err := runner.Run(t.Context(), request(database, `SELECT g FROM generate_series(1, 5000) g`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(result.Rows) != queryrunner.DefaultLimits().MaxRows {
		t.Fatalf("rows = %d, want %d", len(result.Rows), queryrunner.DefaultLimits().MaxRows)
	}
	if !result.Truncated {
		t.Fatal("a cut result did not say it was cut")
	}
}

// Exactly at the limit is the boundary the flag is easiest to get wrong at: a
// result of exactly MaxRows is complete, not cut.
func TestAResultExactlyAtTheLimitIsNotCut(t *testing.T) {
	runner, database := setup(t)
	limit := queryrunner.DefaultLimits().MaxRows

	result, err := runner.Run(t.Context(),
		request(database, `SELECT g FROM generate_series(1, `+itoa(limit)+`) g`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(result.Rows) != limit {
		t.Fatalf("rows = %d, want %d", len(result.Rows), limit)
	}
	if result.Truncated {
		t.Fatal("a complete result was reported as cut")
	}
}

// A thousand rows can still be an enormous answer if each one is a megabyte.
func TestAHugeResultIsCutByItsSize(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = 64 << 10
	runner, database := setupWith(t, limits, checker.NewChecker())

	result, err := runner.Run(t.Context(),
		request(database, `SELECT repeat('x', 4096) FROM generate_series(1, 100)`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if !result.Truncated {
		t.Fatal("an oversized result was not cut")
	}
	if len(result.Rows) >= 100 {
		t.Fatalf("rows = %d; the size limit did nothing", len(result.Rows))
	}
}

// The point of the whole component, and the thing the database layer cannot
// do for itself.
//
// statement_timeout is USERSET: SQL that reached the database unchecked turns
// it off in one statement. The runner's deadline lives on the connection's
// context, which no SQL can reach. The role's own timeout is five seconds, so
// a run that ends in well under one proves which of the two bounded it.
func TestTheRunnersOwnDeadlineIsWhatBoundsTime(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 400 * time.Millisecond
	// pg_sleep is deliberately not on the standard allow-list. Extending the
	// checker here is what lets this test reach the database at all, and it is
	// the point: the deadline must hold for a query the validator did not stop.
	runner, database := setupWith(t, limits, checker.NewChecker("pg_sleep"))

	started := time.Now()
	_, err := runner.Run(t.Context(), request(database, `SELECT pg_sleep(30)`))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a thirty-second query finished")
	}
	if !errors.Is(err, queryrunner.ErrTimeout) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took %s — the role's five-second timeout bounded it, not the runner", elapsed)
	}
}

// A deadline that only abandons the connection leaves the query running: the
// slot is free on our side and spent on the server's, which is the failure
// admission control is meant to prevent.
func TestATimedOutQueryStopsRunningOnTheServer(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 400 * time.Millisecond
	runner, database := setupWith(t, limits, checker.NewChecker("pg_sleep"))

	if _, err := runner.Run(t.Context(), request(database, `SELECT pg_sleep(30)`)); err == nil {
		t.Fatal("a thirty-second query finished")
	}

	admin := gamedbtest.Admin(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var running int
		if err := admin.QueryRow(t.Context(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND state = 'active'`,
			database).Scan(&running); err != nil {
			t.Fatalf("asking the server what is running: %v", err)
		}
		if running == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d query still running on the server after the client gave up", running)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestARefusedQueryNeverReachesTheDatabase(t *testing.T) {
	runner, database := setup(t)

	_, err := runner.Run(t.Context(), request(database, `DROP TABLE evidence`))
	var refusal *sqlpolicy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}

	// And the table is still there, which is the part that matters.
	result, err := runner.Run(t.Context(), request(database, `SELECT count(*) FROM evidence`))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d", len(result.Rows))
	}
}

// A function that builds a value from a number is where one query could make
// a game-cluster process allocate close to a gigabyte, and nothing on this
// side of the connection stops that once the query has been sent: the result
// budget and the deadline only discard what the server already built. So the
// bound has to hold before the query is sent, and this proves it does on the
// real path — refused with the declared code, and never executed — while the
// bounded forms still run and PostgreSQL agrees with the checker about what
// they produce.
func TestAnUnboundedGeneratorNeverReachesTheCluster(t *testing.T) {
	runner, database := setup(t)
	gamedbtest.Run(t, database, `GRANT INSERT ON evidence TO `+gamedb.RoleWriter)

	// Never executed, not merely failed: a write one past the bound would
	// leave a row behind had it reached the database. First, and fatal, so
	// that a checker without the bound stops the test here rather than
	// sending the gigabyte-sized queries below to the cluster.
	writing := request(database, `INSERT INTO evidence (id, note) SELECT 3, repeat('x', 10001)`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	if _, err := runner.Run(t.Context(), writing); err == nil {
		t.Fatal("a write with an unbounded repeat was allowed")
	}
	result, err := runner.Run(t.Context(), request(database, `SELECT count(*) FROM evidence`))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got := result.Rows[0][0]; got != int64(2) {
		t.Fatalf("count after the refused write = %v, want 2: the query reached the database", got)
	}

	for _, sql := range []string{
		`SELECT length(repeat('x', 900000000))`,
		`SELECT length(rpad('', 900000000, 'x'))`,
		`SELECT length(format('%900000000s', ''))`,
		`SELECT length(string_agg(repeat('x', 1000000), ',')) FROM generate_series(1, 2000)`,
		`SELECT cardinality(array_agg(repeat('x', 1000000))) FROM generate_series(1, 2000)`,
		`SELECT count(*) FROM generate_series(1, 100000000)`,
		`SELECT length(repeat(note, id * 100000000)) FROM evidence`,
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := runner.Run(t.Context(), request(database, sql))
			var refusal *sqlpolicy.Refusal
			if !errors.As(err, &refusal) || refusal.Code != sqlpolicy.CodeArgumentNotBounded {
				t.Fatalf("error = %v, want a refusal with code %q", err, sqlpolicy.CodeArgumentNotBounded)
			}
		})
	}

	// The bounded forms run, and produce what the checker assumed they would.
	for sql, want := range map[string]int64{
		`SELECT length(repeat('x', 10000))`:                                                      10_000,
		`SELECT length(lpad('7', 10000, '0'))`:                                                   10_000,
		`SELECT length(format('%5000s|%4999s', 'a', 'b'))`:                                       10_000,
		`SELECT count(*) FROM generate_series(1, 100000)`:                                        100_000,
		`SELECT count(*) FROM generate_series(1, 1000000, 10)`:                                   100_000,
		`SELECT count(*) FROM generate_series('2024-01-01'::date, '2024-12-31'::date, '1 day')`:  366,
		`SELECT count(*) FROM generate_series('2024-01-01', '2024-01-02', '01:00:00'::interval)`: 25,
		`SELECT count(*) FROM generate_series('2024-03-01 00:00+02'::timestamptz, '2024-04-01 00:00+03'::timestamptz, '1 hour', 'Europe/Chisinau')`: 744,
		`SELECT count(*) FROM generate_series('2020-01-01'::timestamp, '2024-01-01'::timestamp, '1 month')`:                                         49,
	} {
		t.Run(sql, func(t *testing.T) {
			result, err := runner.Run(t.Context(), request(database, sql))
			if err != nil {
				t.Fatalf("a bounded generator failed: %v", err)
			}
			if got := result.Rows[0][0]; got != want && got != int32(want) {
				t.Fatalf("%s = %v (%T), want %d", sql, got, got, want)
			}
		})
	}
}

func TestAnUnknownDatabaseFailsWithoutPanicking(t *testing.T) {
	runner, _ := setup(t)

	if _, err := runner.Run(t.Context(), request("no_such_database", `SELECT 1`)); err == nil {
		t.Fatal("a query against a missing database succeeded")
	}
}

// A caller going away is not a query running too long, and recording it as one
// inflates the very number capacity decisions are made from.
//
// The two are easy to conflate because both cancel the context: the check was
// `ctx.Err() != nil`, which is true for either. What tells them apart is which
// error the context carries.
func TestACallerGoingAwayIsNotATimeout(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 30 * time.Second // far longer than this test will wait
	runner, database := setupWith(t, limits, checker.NewChecker("pg_sleep"))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	_, err := runner.Run(ctx, request(database, `SELECT pg_sleep(30)`))

	if errors.Is(err, queryrunner.ErrTimeout) {
		t.Fatalf("a cancelled request was reported as a timeout: %v", err)
	}
	if !errors.Is(err, queryrunner.ErrCanceled) {
		t.Fatalf("error = %v, want ErrCanceled", err)
	}
}

// The semaphore bounds what is running; this bounds how often one person asks.
// A thousand cheap queries pass the semaphore one at a time, which is why the
// two are separate layers rather than one.
func TestAParticipantMayNotAskFasterThanTheContestAllows(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.PerMinute = 3
	runner, database := setupWith(t, limits, checker.NewChecker())

	for i := range 3 {
		if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); err != nil {
			t.Fatalf("query %d was refused: %v", i+1, err)
		}
	}
	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries", err)
	}
}

// The quota is checked before a write and never before a read: a read cannot
// fill a disk, and the check costs a round trip on every query.
func TestAWriteIsRefusedWhenTheDatabaseIsAtItsLimit(t *testing.T) {
	runner, database := setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := request(database, `INSERT INTO evidence (id, note) VALUES (99, 'planted')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	// One byte: any real database is past it.
	writing.DiskQuotaBytes = 1

	if _, err := runner.Run(t.Context(), writing); !errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatalf("error = %v, want ErrDiskFull", err)
	}

	// Reading is unaffected, which is the half that matters during a contest:
	// a participant who has filled their database can still look at it.
	reading := request(database, `SELECT count(*) FROM evidence`)
	reading.Policy = sqlpolicy.ReadWrite("evidence")
	reading.DiskQuotaBytes = 1
	if _, err := runner.Run(t.Context(), reading); err != nil {
		t.Fatalf("reading was refused by the size limit: %v", err)
	}
}

// And a quota nobody set is no quota, which is what a read-only contest wants.
func TestNoQuotaMeansNoCheck(t *testing.T) {
	runner, database := setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := request(database, `INSERT INTO evidence (id, note) VALUES (98, 'x')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	// It fails on privileges, not on size: the writer was granted no INSERT on
	// the game table in this arrangement.
	if _, err := runner.Run(t.Context(), writing); errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatal("a database with no quota was refused for its size")
	}
}

// The point of a read-write contest: what a participant writes stays written.
//
// Both halves of this used to fail. A write was wrapped in the same subquery a
// read is, which is a syntax error for an INSERT, and had it run, the
// transaction was rolled back unconditionally. Neither was visible to a test
// that connected as the reader, whose missing grant refused the write first.
func TestAPermittedWriteIsKept(t *testing.T) {
	runner, database := setup(t)
	gamedbtest.Run(t, database, `GRANT INSERT, UPDATE, DELETE ON evidence TO `+gamedb.RoleWriter)

	writing := request(database, `INSERT INTO evidence (id, note) VALUES (3, 'a glove'), (4, 'a coat')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	result, err := runner.Run(t.Context(), writing)
	if err != nil {
		t.Fatalf("a permitted write failed: %v", err)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("rows affected = %d, want 2", result.RowsAffected)
	}

	// Read back through the runner, on a fresh connection: what the
	// transaction committed is what another query sees.
	reading := request(database, `SELECT count(*) FROM evidence`)
	reading.Policy = sqlpolicy.ReadWrite("evidence")
	result, err = runner.Run(t.Context(), reading)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got := result.Rows[0][0]; got != int64(4) {
		t.Fatalf("count after the write = %v, want 4", got)
	}
}

// A write that answers with rows answers with rows: RETURNING is read through
// the same limits a SELECT is, and its count is the row count.
func TestAWriteWithReturningAnswersWithRows(t *testing.T) {
	runner, database := setup(t)
	gamedbtest.Run(t, database, `GRANT UPDATE ON evidence TO `+gamedb.RoleWriter)

	writing := request(database, `UPDATE evidence SET note = upper(note) RETURNING id, note`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	result, err := runner.Run(t.Context(), writing)
	if err != nil {
		t.Fatalf("a permitted UPDATE … RETURNING failed: %v", err)
	}
	if len(result.Rows) != 2 || result.RowsAffected != 0 {
		t.Fatalf("rows = %d, affected = %d; want the rows to be the answer", len(result.Rows), result.RowsAffected)
	}
}

// A participant's own table lives in `work`, and making one is a write that
// has to be committed like any other.
func TestOwnTablesAreKeptInWork(t *testing.T) {
	runner, database := setup(t)
	policy := sqlpolicy.ReadWrite()
	policy.AllowOwnTables = true

	creating := request(database, `CREATE TABLE work.notes (id int, body text)`)
	creating.Policy = policy
	if _, err := runner.Run(t.Context(), creating); err != nil {
		t.Fatalf("creating an own table: %v", err)
	}

	inserting := request(database, `INSERT INTO work.notes VALUES (1, 'the butler')`)
	inserting.Policy = policy
	if _, err := runner.Run(t.Context(), inserting); err != nil {
		t.Fatalf("writing to an own table: %v", err)
	}

	reading := request(database, `SELECT body FROM work.notes`)
	reading.Policy = policy
	result, err := runner.Run(t.Context(), reading)
	if err != nil {
		t.Fatalf("reading an own table: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0] != "the butler" {
		t.Fatalf("rows = %v, want the note that was written", result.Rows)
	}
}

// Which role a query runs as is the policy's decision, and it has to be: the
// writer's grants are what make a read-write contest's writes possible, and
// the reader's lack of them is what keeps a read-only contest read-only by
// privilege rather than by the transaction's access mode alone.
//
// Proven through a table only the writer may read: the same query is refused
// by the database under one policy and answered under the other, and nothing
// but the role behind the connection differs.
func TestTheRoleFollowsThePolicy(t *testing.T) {
	runner, database := setup(t)
	// Created after the fixture's GRANT … ON ALL TABLES, so neither role can
	// read it until this says so; only the writer is told.
	gamedbtest.Run(t, database,
		`CREATE TABLE writer_only (id int)`,
		`GRANT SELECT ON writer_only TO `+gamedb.RoleWriter,
	)

	if _, err := runner.Run(t.Context(), request(database, `SELECT id FROM writer_only`)); err == nil {
		t.Fatal("a read-only contest read a table only the writer may see: it ran as the writer")
	}

	asWriter := request(database, `SELECT id FROM writer_only`)
	asWriter.Policy = sqlpolicy.ReadWrite("evidence")
	if _, err := runner.Run(t.Context(), asWriter); err != nil {
		t.Fatalf("a read-write contest could not read as the writer: %v", err)
	}
}

// Without writer credentials a read-write contest is refused, not quietly run
// as the reader, whose missing grants would turn every permitted write into
// "permission denied" and read as a bug in the contest.
func TestARunnerWithoutAWriterRefusesAReadWriteContest(t *testing.T) {
	database := gamedbtest.Scratch(t)
	gamedbtest.Run(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY)`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader,
	)
	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword(t), database), "")
	if err != nil {
		t.Fatalf("building the cluster connector: %v", err)
	}
	runner := queryrunner.New(cluster, checker.NewChecker(), queryrunner.DefaultLimits())

	reading := request(database, `SELECT id FROM evidence`)
	reading.Policy = sqlpolicy.ReadWrite("evidence")
	if _, err := runner.Run(t.Context(), reading); !errors.Is(err, queryrunner.ErrNoWriter) {
		t.Fatalf("error = %v, want ErrNoWriter", err)
	}

	// A read-only contest is unaffected: the reader is all it needs.
	if _, err := runner.Run(t.Context(), request(database, `SELECT id FROM evidence`)); err != nil {
		t.Fatalf("a read-only contest was refused: %v", err)
	}
}

// One statement to the parser, a syntax error inside a FROM: the wrapper has
// to cut where the parser said the statement ended, not where the string does.
func TestAStatementFollowedByACommentStillRuns(t *testing.T) {
	runner, database := setup(t)

	for name, sql := range map[string]string{
		"a semicolon then a comment": "SELECT 1 AS a; -- and a note",
		"a semicolon then a newline": "SELECT 1 AS a;\n\n",
		"a leading comment":          "-- what I was thinking\nSELECT 1 AS a",
		"a leading block comment":    "/* thinking */ SELECT 1 AS a;",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := runner.Run(t.Context(), request(database, sql))
			if err != nil {
				t.Fatalf("%q: %v", sql, err)
			}
			if len(result.Rows) != 1 {
				t.Fatalf("%q: rows = %d, want 1", sql, len(result.Rows))
			}
		})
	}
}

// The result budget bounds memory, not only what is passed on.
//
// The driver reads a whole row before handing any of it over, so a check on
// the values it decoded comes after the allocation it exists to prevent: one
// cell of hundreds of megabytes is hundreds of megabytes in this process. The
// budget is therefore enforced where the bytes arrive. A single cell larger
// than the whole allowance cannot be shown in part, so it is refused by name.
func TestOneCellLargerThanTheBudgetIsRefusedWithoutBeingRead(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = 64 << 10
	runner, database := setupWith(t, limits, checker.NewChecker())

	// Eight megabytes in one cell: past the budget and past its slack, and
	// small enough that reading it whole would not itself fail the test —
	// what fails the test is reading it at all.
	_, err := runner.Run(t.Context(), request(database, `SELECT repeat('x', 8 * 1024 * 1024)`))
	if !errors.Is(err, queryrunner.ErrResultTooLarge) {
		t.Fatalf("error = %v, want ErrResultTooLarge", err)
	}

	// The connection that refused to read is closed with the query; the next
	// one starts with a fresh budget.
	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); err != nil {
		t.Fatalf("the runner did not recover: %v", err)
	}
}

// A database name is the caller's and is checked upstream; the runner still
// refuses to build a connection string out of anything but a plain name.
func TestADatabaseNameThatIsNotPlainIsRefused(t *testing.T) {
	runner, _ := setup(t)

	_, err := runner.Run(t.Context(), request("game?sslmode=require", `SELECT 1`))
	if err == nil {
		t.Fatal("a database name with a query string in it was accepted")
	}
}

// The rate bounds how often the parser is exercised, so a refused query counts
// too: otherwise refusals would be free, and the parser is C code reading text
// an adversary chose.
func TestARefusedQueryStillCountsAgainstTheRate(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.PerMinute = 2
	runner, database := setupWith(t, limits, checker.NewChecker())

	for range 2 {
		if _, err := runner.Run(t.Context(), request(database, `COPY evidence TO STDOUT`)); errors.Is(err, queryrunner.ErrTooManyQueries) {
			t.Fatal("refused within the rate")
		}
	}
	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries after two refused queries", err)
	}
}

// The console prints a column's type under its name, beside a schema panel
// that got its own from format_type(). This is the assertion that the two
// panels of one screen agree: `timestamp with time zone`, not the driver's
// `timestamptz`, and resolved from the row description the query already
// carried rather than from a second trip to the catalogue.
func TestAResultNamesEachColumnsType(t *testing.T) {
	runner, database := setup(t)

	result, err := runner.Run(t.Context(), request(database,
		`SELECT 'Margot'::text AS full_name, now() AS at, 1 AS n FROM evidence LIMIT 1`))
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	want := []string{"text", "timestamp with time zone", "integer"}
	if len(result.ColumnTypes) != len(result.Columns) {
		t.Fatalf("%d columns but %d types: %v / %v",
			len(result.Columns), len(result.ColumnTypes), result.Columns, result.ColumnTypes)
	}
	for i := range want {
		if result.ColumnTypes[i] != want[i] {
			t.Fatalf("type of %q = %q, want %q (all: %v)",
				result.Columns[i], result.ColumnTypes[i], want[i], result.ColumnTypes)
		}
	}
}

// The meter under the editor says how long the query took. Two things have to
// hold for that number to be worth printing: it is not zero, and it is a
// proper part of the call rather than the whole of it — opening the
// connection, beginning the transaction and answering over the wire are this
// platform's costs, not the participant's query's.
func TestAResultSaysHowLongTheStatementTook(t *testing.T) {
	runner, database := setup(t)

	before := time.Now()
	result, err := runner.Run(t.Context(), request(database, `SELECT id FROM evidence`))
	whole := time.Since(before)
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	if result.Duration <= 0 {
		t.Fatalf("duration = %v; the statement was not timed at all", result.Duration)
	}

	// The untimed part of the call has to be the larger half, and that is a
	// fact about round trips rather than about this machine's speed. Opening
	// a connection is a TCP handshake, an authentication exchange and a
	// startup packet — three round trips before any SQL is sent — while
	// reading two rows out of a two-row table is one. A duration that had the
	// connection folded into it leaves almost nothing outside itself, which
	// is the shape this catches.
	if outside := whole - result.Duration; outside < result.Duration {
		t.Fatalf("the statement was timed at %v of a %v call, leaving only %v for "+
			"opening the connection and beginning the transaction; those are this "+
			"platform's costs and are being charged to the participant's query",
			result.Duration, whole, outside)
	}
}

// And it has to be a measurement rather than a constant: a statement that
// makes the server do real work registers more than a trivial one. Two
// million rows counted server-side is tens of milliseconds; reading two rows
// out of a tiny table is well under one.
func TestTheDurationMeasuresTheStatementAndNotSomethingConstant(t *testing.T) {
	runner, database := setup(t)

	cheap, err := runner.Run(t.Context(), request(database, `SELECT id FROM evidence`))
	if err != nil {
		t.Fatalf("running the cheap query: %v", err)
	}
	costly, err := runner.Run(t.Context(),
		request(database, `SELECT count(*) FROM generate_series(1, 2000000)`))
	if err != nil {
		t.Fatalf("running the costly query: %v", err)
	}

	if costly.Duration <= cheap.Duration {
		t.Fatalf("counting two million rows took %v and reading two rows took %v; "+
			"the duration is not measuring the statement", costly.Duration, cheap.Duration)
	}
}
