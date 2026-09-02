package queryrunner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
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
	runner, database := setupWith(t, limits, sqlpolicy.NewChecker())

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
	runner, database := setupWith(t, limits, sqlpolicy.NewChecker("pg_sleep"))

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
	runner, database := setupWith(t, limits, sqlpolicy.NewChecker("pg_sleep"))

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
	runner, database := setupWith(t, limits, sqlpolicy.NewChecker("pg_sleep"))

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
