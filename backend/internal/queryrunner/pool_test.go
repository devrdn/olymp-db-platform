package queryrunner_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// These tests observe kept connections from the server's side
// (pg_backend_pid, pg_stat_activity), not from a counter inside the pool.

func TestAReadIsServedByTheConnectionThePreviousReadLeft(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})

	first := backend(t, runner, database)
	if second := backend(t, runner, database); second != first {
		t.Fatalf("the second read ran on backend %d, the first on %d: nothing was kept", second, first)
	}
}

// The statements are ones the checker refuses, run through `anything`, so the
// reset is proved on its own. Each case first proves the backend was reused.
func TestSessionStateDoesNotOutliveTheQueryThatLeftIt(t *testing.T) {
	for name, tc := range map[string]struct{ leave, check string }{
		"a session advisory lock": {
			leave: `SELECT pg_try_advisory_lock(42)`,
			// Unlocking answers true only for a lock this session holds.
			check: `SELECT count(*) WHERE pg_advisory_unlock(42)`,
		},
		"a prepared statement": {
			leave: `PREPARE left_behind AS SELECT 1`,
			check: `SELECT count(*) FROM pg_prepared_statements`,
		},
		// A session lock outlives the read's rollback; only the reset frees it.
		"a shared session advisory lock": {
			leave: `SELECT pg_advisory_lock_shared(43)`,
			check: `SELECT count(*) WHERE pg_advisory_unlock_shared(43)`,
		},
		// A driver statement cache would make the count include the check.
		"a statement the driver prepared": {
			leave: `SELECT id FROM evidence WHERE id = 1`,
			check: `SELECT count(*) FROM pg_prepared_statements`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			runner, database := setupWith(t, unlimited(), anything{})

			first := backend(t, runner, database)
			if _, err := runner.Run(t.Context(), request(database, tc.leave)); err != nil {
				t.Fatalf("%s: %v", tc.leave, err)
			}
			result, err := runner.Run(t.Context(), request(database,
				`SELECT pg_backend_pid(), (`+tc.check+`)`))
			if err != nil {
				t.Fatalf("checking: %v", err)
			}
			if pid := result.Rows[0][0].(int32); pid != first {
				t.Fatalf("the check ran on backend %d, not %d: the case proves nothing", pid, first)
			}
			if left := result.Rows[0][1].(int64); left != 0 {
				t.Fatalf("%s: the next query on the same backend still sees it (%d)", tc.leave, left)
			}
		})
	}
}

// A reset is not trusted to find what a write leaves, such as a committed
// temporary table.
func TestAReadWriteContestNeverSharesAConnection(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})
	gamedbtest.Run(t, database, `GRANT TEMPORARY ON DATABASE `+sqlpolicy.QuoteIdentifier(database)+` TO `+gamedb.RoleWriter)

	writing := func(sql string) *queryrunner.Result {
		t.Helper()
		req := request(database, sql)
		req.Policy = sqlpolicy.ReadWrite("evidence")
		result, err := runner.Run(t.Context(), req)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return result
	}

	writing(`CREATE TEMP TABLE left_behind (x int) ON COMMIT PRESERVE ROWS`)
	result := writing(`SELECT to_regclass('pg_temp.left_behind') IS NULL`)
	if gone := result.Rows[0][0].(bool); !gone {
		t.Fatal("a temporary table one write created was visible to the next")
	}
	if n := eventually(t, 0, database); n != 0 {
		t.Fatalf("a read-write contest left %d connections open", n)
	}
}

// An abandoned query may have a cancel in flight that would hit the next query.
func TestAConnectionWhoseQueryDidNotFinishCleanlyIsNotReused(t *testing.T) {
	for name, end := range map[string]func(t *testing.T, runner *queryrunner.Runner, database string){
		"cancelled by the caller": func(t *testing.T, runner *queryrunner.Runner, database string) {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			if _, err := runner.Run(ctx, request(database, `SELECT pg_sleep(5)`)); !errors.Is(err, queryrunner.ErrCanceled) && !errors.Is(err, queryrunner.ErrTimeout) {
				t.Fatalf("error = %v, want the query abandoned", err)
			}
		},
		"past the runner's deadline": func(t *testing.T, runner *queryrunner.Runner, database string) {
			if _, err := runner.Run(t.Context(), request(database, `SELECT pg_sleep(5)`)); !errors.Is(err, queryrunner.ErrTimeout) {
				t.Fatalf("error = %v, want ErrTimeout", err)
			}
		},
		"refused by the database": func(t *testing.T, runner *queryrunner.Runner, database string) {
			if _, err := runner.Run(t.Context(), request(database, `SELECT 1/0`)); err == nil {
				t.Fatal("a division by zero succeeded")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			limits := unlimited()
			limits.Deadline = 300 * time.Millisecond
			runner, database := setupWith(t, limits, anything{})

			first := backend(t, runner, database)
			if again := backend(t, runner, database); again != first {
				t.Fatalf("backend %d then %d: the pool is not in use, so the case proves nothing", first, again)
			}

			end(t, runner, database)

			if after := backend(t, runner, database); after == first {
				t.Fatalf("backend %d served the query after one that did not finish cleanly", after)
			}
		})
	}
}

// A kept connection must not stop the reclaim sweep's plain DROP DATABASE.
func TestAnIdleConnectionIsClosedAfterItsIdleTimeout(t *testing.T) {
	limits := unlimited()
	limits.IdleTimeout = 300 * time.Millisecond
	runner, database := setupWith(t, limits, anything{})

	backend(t, runner, database)
	if n := participantBackends(t, database); n != 1 {
		t.Fatalf("right after a read the runner holds %d connections, want the one it kept", n)
	}
	if n := eventually(t, 0, database); n != 0 {
		t.Fatalf("after the idle timeout the runner still holds %d connections", n)
	}

	// Without FORCE, as the reclaim sweep drops.
	if _, err := gamedbtest.Admin(t).Exec(t.Context(), `DROP DATABASE `+sqlpolicy.QuoteIdentifier(database)); err != nil {
		t.Fatalf("a plain DROP DATABASE after the idle timeout: %v", err)
	}
}

// At the bound, a read needing a new connection closes the least recently used
// kept one first.
func TestTheRunnerKeepsNoMoreConnectionsThanItRunsQueries(t *testing.T) {
	limits := unlimited()
	limits.Concurrent = 2
	runner, first := setupWith(t, limits, anything{})
	databases := []string{first, seeded(t), seeded(t), seeded(t)}

	for i, database := range databases {
		backend(t, runner, database)
		want := min(i+1, limits.Concurrent)
		if n := eventually(t, want, databases...); n != want {
			t.Fatalf("after reading %d databases the runner holds %d connections, want %d", i+1, n, want)
		}
	}

	// The two kept are the two most recently used.
	if n := participantBackends(t, databases[2:]...); n != 2 {
		t.Fatalf("the most recently used databases hold %d connections, want 2", n)
	}
}

// Sampled from the server's side for the whole run.
func TestTheBoundHoldsWhileParticipantsAskAtOnce(t *testing.T) {
	limits := unlimited()
	limits.Concurrent = 2
	limits.QueueDepth = 16
	runner, first := setupWith(t, limits, anything{})
	databases := []string{first, seeded(t), seeded(t), seeded(t), seeded(t), seeded(t)}

	var peak atomic.Int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	admin := gamedbtest.Admin(t)
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n int64
			if err := admin.QueryRow(context.Background(),
				`SELECT count(*) FROM pg_stat_activity WHERE usename = $1 AND datname = ANY($2)`,
				gamedb.RoleReader, databases).Scan(&n); err == nil && n > peak.Load() {
				peak.Store(n)
			}
		}
	}()

	var wg sync.WaitGroup
	deadline := time.Now().Add(3 * time.Second)
	for _, database := range databases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := request(database, `SELECT pg_sleep(0.01)`)
			req.Registration = uuid.New()
			for time.Now().Before(deadline) {
				if _, err := runner.Run(context.Background(), req); err != nil {
					t.Errorf("%s: %v", database, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-sampled

	if got := peak.Load(); got > int64(limits.Concurrent) {
		t.Fatalf("peak participant backends = %d, over the bound of %d", got, limits.Concurrent)
	}
	if peak.Load() == 0 {
		t.Fatal("the sampler never saw a participant backend: the test proves nothing")
	}
}

// The gate makes this unreachable through Run, so the pool is driven directly:
// a broken gate must show up as a refusal, not as extra backends.
func TestThePoolRefusesToGoPastItsBound(t *testing.T) {
	limits := unlimited()
	limits.Concurrent = 1
	runner, first := setupWith(t, limits, anything{})
	second := seeded(t)

	release, err := queryrunner.HoldConnection(t.Context(), runner, first)
	if err != nil {
		t.Fatalf("holding the one connection: %v", err)
	}
	if _, err := queryrunner.HoldConnection(t.Context(), runner, second); !errors.Is(err, queryrunner.ErrBusy) {
		t.Fatalf("a second connection past a bound of one: error = %v, want ErrBusy", err)
	}
	if n := participantBackends(t, first, second); n != 1 {
		t.Fatalf("the runner holds %d connections, want 1", n)
	}

	// Kept idle, so it may now be closed to make room.
	release(true)
	again, err := queryrunner.HoldConnection(t.Context(), runner, second)
	if err != nil {
		t.Fatalf("a connection in place of an idle one: %v", err)
	}
	defer again(false)
	if n := eventually(t, 1, first, second); n != 1 {
		t.Fatalf("the runner holds %d connections, want 1", n)
	}
}

// A write runs as another role, and holding two connections to one instance
// would use up its CONNECTION LIMIT 2.
func TestAWriteClosesTheReadConnectionKeptForItsDatabase(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})

	backend(t, runner, database)
	if n := participantBackends(t, database); n != 1 {
		t.Fatalf("after a read the runner keeps %d connections, want 1", n)
	}

	writing := request(database, `SELECT 1`)
	writing.Policy = sqlpolicy.ReadWrite("evidence")
	if _, err := runner.Run(t.Context(), writing); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if n := eventually(t, 0, database); n != 0 {
		t.Fatalf("after a write the read connection kept for the same database is still open (%d)", n)
	}
}

func TestClosingTheRunnerClosesItsIdleConnections(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})

	backend(t, runner, database)
	runner.Close()

	if n := eventually(t, 0, database); n != 0 {
		t.Fatalf("a closed runner still holds %d connections", n)
	}
}
