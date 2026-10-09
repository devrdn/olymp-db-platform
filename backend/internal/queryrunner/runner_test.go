package queryrunner_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

// The limiting wrapper is string surgery on the participant's SQL.
func TestTheShapesThatBreakAWrapper(t *testing.T) {
	runner, database := setup(t)

	for name, sql := range map[string]string{
		"a trailing line comment": "SELECT 1 AS a -- what I was thinking",
		"a trailing semicolon":    `SELECT 1 AS a;`,
		"trailing whitespace":     "SELECT 1 AS a   \n\t ",
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

// EXPLAIN cannot go inside a subquery.
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

// statement_timeout is USERSET, so SQL can turn it off. The role's timeout is
// five seconds, so ending well under one proves the runner's deadline did it.
func TestTheRunnersOwnDeadlineIsWhatBoundsTime(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 400 * time.Millisecond
	// pg_sleep is not on the standard allow-list; the deadline must hold for a
	// query the validator did not stop.
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

// Abandoning the connection alone would free the slot here while the server
// still spends it.
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

	// The table is still there.
	result, err := runner.Run(t.Context(), request(database, `SELECT count(*) FROM evidence`))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d", len(result.Rows))
	}
}

// The checker refuses a constant size that is plainly too large before the
// query is sent.
func TestAConstantOverTheBoundNeverReachesTheCluster(t *testing.T) {
	runner, database := setup(t)

	for _, sql := range []string{
		`SELECT length(repeat('x', 900000000))`,
		`SELECT count(*) FROM generate_series(1, 100000000)`,
		`SELECT length(string_agg(repeat('x', 1000000), ',')) FROM generate_series(1, 2000)`,
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := runner.Run(t.Context(), request(database, sql))
			var refusal *sqlpolicy.Refusal
			if !errors.As(err, &refusal) || refusal.Code != sqlpolicy.CodeArgumentNotBounded {
				t.Fatalf("error = %v, want a refusal with code %q", err, sqlpolicy.CodeArgumentNotBounded)
			}
		})
	}
}

// The game cluster's per-process memory cap (deploy/docker-compose.dev.yml
// ulimits.data mirrors pg-game's) is the real bound, proved on the deployment's
// configuration (CLAUDE.md rule 10). A query the validator admits but that
// builds gigabytes must fail with "out of memory" in its own backend, without
// the postmaster restarting.
func TestAnOverAllocatingQueryFailsInItsOwnBackend(t *testing.T) {
	runner, database := setup(t)
	admin := gamedbtest.Admin(t)

	// Held open across every probe: a postmaster restart would kill its
	// backend, which a pool would hide by handing back a fresh connection.
	watcher, err := admin.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquiring a watcher connection: %v", err)
	}
	defer watcher.Release()
	var watcherPID int
	if err := watcher.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&watcherPID); err != nil {
		t.Fatalf("reading the watcher backend pid: %v", err)
	}
	startTime := postmasterStart(t, admin)

	// Each is admitted by the validator and asks for far more than the cap.
	for name, sql := range map[string]string{
		"the bit-cast bypass":         `SELECT length(repeat('x', (-173741824)::bit(30)::int))`,
		"array_agg over a cross join": `SELECT cardinality(array_agg(repeat('x', 10000))) FROM generate_series(1, 100000) a, generate_series(1, 10) b`,
		"several aggregates at once":  `SELECT length(string_agg(repeat('x', 10000), '')), length(string_agg(repeat('y', 10000), '')) FROM generate_series(1, 100000)`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runner.Run(t.Context(), request(database, sql))
			if err == nil {
				t.Fatalf("an over-allocating query was allowed to finish: %s", sql)
			}
			// Not a validator refusal: the database declined it.
			var refusal *sqlpolicy.Refusal
			if errors.As(err, &refusal) {
				t.Fatalf("refused by the validator, not the cluster: %v", err)
			}
			// An out-of-memory SQLSTATE, not the broken connection a killed
			// backend would give.
			var pg *pgconn.PgError
			if !errors.As(err, &pg) {
				t.Fatalf("error = %v (%T), want PostgreSQL's own out-of-memory error", err, err)
			}
			if pg.Code != "53200" {
				t.Fatalf("SQLSTATE = %s (%s), want 53200 out_of_memory", pg.Code, pg.Message)
			}
		})
	}

	// The postmaster never restarted: same start time, same watcher backend.
	if now := postmasterStart(t, admin); !now.Equal(startTime) {
		t.Fatalf("the postmaster restarted: %s -> %s; a backend was killed rather than told no", startTime, now)
	}
	var samePID int
	if err := watcher.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&samePID); err != nil {
		t.Fatalf("the watcher connection did not survive the probes (crash recovery?): %v", err)
	}
	if samePID != watcherPID {
		t.Fatalf("the watcher backend changed pid %d -> %d: the cluster restarted", watcherPID, samePID)
	}
	// Other participants are untouched.
	if _, err := runner.Run(t.Context(), request(database, `SELECT count(*) FROM evidence`)); err != nil {
		t.Fatalf("the cluster did not serve a normal query after the over-allocating ones: %v", err)
	}
}

// A smoke test: one participant abandons query after query, and live backends
// stay at about one. It passes with either cancel mechanism removed; a client
// that vanishes with no cancel is covered in internal/gamedb
// (TestAnAbruptlyAbandonedBackendStopsWithinTheCheckInterval).
func TestAbandonedQueriesDoNotOutliveTheirSlot(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	runner, database := setupWith(t, limits, checker.NewChecker())
	admin := gamedbtest.Admin(t)

	// Long and cheap, so cancellation ends it, not out-of-memory.
	const slow = `SELECT count(*) FROM generate_series(1, 100000) a, generate_series(1, 100000) b`

	// Sample the peak through the burst and the 5s statement_timeout tail,
	// where uncancelled backends would pile up.
	stop := make(chan struct{})
	done := make(chan struct{})
	var peak int64
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var active int
			err := admin.QueryRow(context.Background(),
				`SELECT count(*) FROM pg_stat_activity
				   WHERE usename = $1 AND state = 'active' AND query NOT LIKE '%pg_stat_activity%'`,
				gamedb.RoleReader).Scan(&active)
			if err == nil && int64(active) > atomic.LoadInt64(&peak) {
				atomic.StoreInt64(&peak, int64(active))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Each Run's context is cancelled while the query still runs on the
	// server, like an aborted HTTP request.
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
		_, _ = runner.Run(ctx, request(database, slow))
		cancel()
	}
	time.Sleep(6 * time.Second)
	close(stop)
	<-done
	// One plus a small tolerance for a backend caught mid-cancellation.
	const want = int64(1)
	tolerance := int64(2)
	if got := atomic.LoadInt64(&peak); got > want+tolerance {
		t.Fatalf("peak live participant backends = %d, over %d + %d tolerance: "+
			"abandoned queries are outliving their slot", got, want, tolerance)
	}
}

// postmasterStart reads when the cluster's postmaster last started.
func postmasterStart(t *testing.T, admin *pgxpool.Pool) time.Time {
	t.Helper()
	var started time.Time
	if err := admin.QueryRow(t.Context(), `SELECT pg_postmaster_start_time()`).Scan(&started); err != nil {
		t.Fatalf("reading the postmaster start time: %v", err)
	}
	return started
}

func TestAnUnknownDatabaseFailsWithoutPanicking(t *testing.T) {
	runner, _ := setup(t)

	if _, err := runner.Run(t.Context(), request("no_such_database", `SELECT 1`)); err == nil {
		t.Fatal("a query against a missing database succeeded")
	}
}

// Both cancel the context; only the context's error tells them apart.
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

// A read cannot fill a disk, so it is never checked.
func TestAWriteIsRefusedWhenTheDatabaseIsAtItsLimit(t *testing.T) {
	runner, database := setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := request(database, `INSERT INTO evidence (id, note) VALUES (99, 'planted')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	// One byte: any real database is past it.
	writing.DiskQuotaBytes = 1

	if _, err := runner.Run(t.Context(), writing); !errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatalf("error = %v, want ErrDiskFull", err)
	}

	// A participant who filled their database can still read it.
	reading := request(database, `SELECT count(*) FROM evidence`)
	reading.Policy = sqlpolicy.ReadWrite("evidence")
	reading.DiskQuotaBytes = 1
	if _, err := runner.Run(t.Context(), reading); err != nil {
		t.Fatalf("reading was refused by the size limit: %v", err)
	}
}

// At the cap a growing write is refused, TRUNCATE is not, pg_database_size
// actually falls, and the refused write then goes through.
func TestAtTheSizeLimitTheWayOutIsStillOpen(t *testing.T) {
	runner, database := setup(t)
	gamedbtest.Run(t, database,
		`GRANT INSERT, UPDATE, DELETE, TRUNCATE ON evidence TO `+gamedb.RoleWriter,
		// About 4 MiB, well past checkpoint noise in pg_database_size.
		`INSERT INTO evidence SELECT g, repeat('x', 512) FROM generate_series(10, 8000) g`,
	)

	policy := sqlpolicy.ReadWrite("evidence")
	quota := databaseSize(t, database)

	growing := request(database, `INSERT INTO evidence (id, note) VALUES (1, 'one more')`)
	growing.Policy, growing.DiskQuotaBytes = policy, quota
	if _, err := runner.Run(t.Context(), growing); !errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatalf("a write that would grow the database at the cap: error = %v, want ErrDiskFull", err)
	}

	freeing := request(database, `TRUNCATE evidence`)
	freeing.Policy, freeing.DiskQuotaBytes = policy, quota
	if _, err := runner.Run(t.Context(), freeing); err != nil {
		t.Fatalf("TRUNCATE at the cap was refused: %v", err)
	}

	if after := databaseSize(t, database); after >= quota {
		t.Fatalf("the database is %d bytes after emptying it, cap %d: nothing was freed", after, quota)
	}

	if _, err := runner.Run(t.Context(), growing); err != nil {
		t.Fatalf("a write after freeing space: %v", err)
	}
}

// databaseSize reads the figure the quota is compared against, from outside
// the runner.
func databaseSize(t *testing.T, database string) int64 {
	t.Helper()

	var size int64
	if err := gamedbtest.Admin(t).QueryRow(t.Context(),
		`SELECT pg_database_size($1)`, database).Scan(&size); err != nil {
		t.Fatalf("reading the size of %s: %v", database, err)
	}
	return size
}

func TestNoQuotaMeansNoCheck(t *testing.T) {
	runner, database := setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())

	writing := request(database, `INSERT INTO evidence (id, note) VALUES (98, 'x')`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")

	// It fails on privileges (no INSERT grant here), not on size.
	if _, err := runner.Run(t.Context(), writing); errors.Is(err, queryrunner.ErrDiskFull) {
		t.Fatal("a database with no quota was refused for its size")
	}
}

// Run as the writer, since the reader's missing grant would hide a failure.
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

	// Read back on a fresh connection.
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

// Proven through a table only the writer may read: the same query is refused
// under one policy and answered under the other.
func TestTheRoleFollowsThePolicy(t *testing.T) {
	runner, database := setup(t)
	// Created after the fixture's grants; only the writer is granted it.
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

	// A read-only contest is unaffected.
	if _, err := runner.Run(t.Context(), request(database, `SELECT id FROM evidence`)); err != nil {
		t.Fatalf("a read-only contest was refused: %v", err)
	}
}

// The wrapper must cut where the parser said the statement ended (CLAUDE.md
// rule 14).
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

// The result budget bounds memory, enforced where the bytes arrive (CLAUDE.md
// rule 12).
func TestOneCellLargerThanTheBudgetIsRefusedWithoutBeingRead(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = 64 << 10
	runner, database := setupWith(t, limits, checker.NewChecker())

	// Ten megabytes in one cell: past the budget and its slack.
	_, err := runner.Run(t.Context(), request(database, `SELECT string_agg(repeat('x', 10000), '') FROM generate_series(1, 1000)`))
	if !errors.Is(err, queryrunner.ErrResultTooLarge) {
		t.Fatalf("error = %v, want ErrResultTooLarge", err)
	}

	// The next query gets a fresh connection and budget.
	if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); err != nil {
		t.Fatalf("the runner did not recover: %v", err)
	}
}

func TestADatabaseNameThatIsNotPlainIsRefused(t *testing.T) {
	runner, _ := setup(t)

	_, err := runner.Run(t.Context(), request("game?sslmode=require", `SELECT 1`))
	if err == nil {
		t.Fatal("a database name with a query string in it was accepted")
	}
}

// The parser is C code reading adversarial text, so refusals are not free
// (CLAUDE.md rule 13).
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

// Types are spelled as format_type() spells them, as the schema panel shows.
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

// The duration is non-zero and excludes the platform's costs around the
// statement.
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

	// Connecting takes several round trips and the statement one, so the
	// untimed part must be the larger half.
	if outside := whole - result.Duration; outside < result.Duration {
		t.Fatalf("the statement was timed at %v of a %v call, leaving only %v for "+
			"opening the connection and beginning the transaction; those are this "+
			"platform's costs and are being charged to the participant's query",
			result.Duration, whole, outside)
	}
}

func TestTheDurationMeasuresTheStatementAndNotSomethingConstant(t *testing.T) {
	runner, database := setup(t)

	cheap, err := runner.Run(t.Context(), request(database, `SELECT id FROM evidence`))
	if err != nil {
		t.Fatalf("running the cheap query: %v", err)
	}
	costly, err := runner.Run(t.Context(),
		request(database, `SELECT count(*) FROM generate_series(1, 100000) a, generate_series(1, 100) b`))
	if err != nil {
		t.Fatalf("running the costly query: %v", err)
	}

	if costly.Duration <= cheap.Duration {
		t.Fatalf("counting ten million rows took %v and reading two rows took %v; "+
			"the duration is not measuring the statement", costly.Duration, cheap.Duration)
	}
}

// The drop severs the kept connection; BEGIN notices and the runner connects
// again rather than failing the query.
func TestARecreatedDatabaseIsNotServedByTheConnectionToTheOldOne(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})
	admin := gamedbtest.Admin(t)

	old := backend(t, runner, database)

	// As provisioning drops an instance.
	if _, err := admin.Exec(t.Context(), `DROP DATABASE `+sqlpolicy.QuoteIdentifier(database)+` WITH (FORCE)`); err != nil {
		t.Fatalf("dropping with a kept connection: %v", err)
	}
	recreate(t, database)
	gamedbtest.Run(t, database, `DELETE FROM evidence WHERE id = 2`)

	result, err := runner.Run(t.Context(), request(database, `SELECT pg_backend_pid(), count(*) FROM evidence`))
	if err != nil {
		t.Fatalf("the first query to the recreated database: %v", err)
	}
	if pid := result.Rows[0][0].(int32); pid == old {
		t.Fatalf("backend %d served the recreated database", pid)
	}
	if n := result.Rows[0][1].(int64); n != 1 {
		t.Fatalf("count = %d, want the recreated database's one row", n)
	}
}
