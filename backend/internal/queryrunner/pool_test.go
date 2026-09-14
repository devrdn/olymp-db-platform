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

// These tests are about the connections the runner keeps between queries, and
// they observe them the only way that proves anything: as the participant's
// role, through the real driver, and from the server's side of the socket
// (pg_backend_pid, pg_stat_activity). A counter inside the pool would say what
// the pool believes; the server says what it holds.

// The handshake is the cost being removed: a participant's next read is served
// by the backend their last one left.
func TestAReadIsServedByTheConnectionThePreviousReadLeft(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})

	first := backend(t, runner, database)
	if second := backend(t, runner, database); second != first {
		t.Fatalf("the second read ran on backend %d, the first on %d: nothing was kept", second, first)
	}
}

// Nothing one query leaves on a session is visible to the next one served by
// the same backend.
//
// The statements here are ones the checker refuses, run through a validator
// that admits anything: the pool's isolation is the layer under the checker and
// has to hold on its own. Each case first proves the backend really was reused,
// or a passing check would prove nothing. Every case is state a read's rollback
// leaves in place, so what each one proves is the reset; the reset's known
// residue is listed in pool's doc.
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
		// Like the exclusive lock above, a session lock outlives the read's
		// rollback: only the reset releases it.
		"a shared session advisory lock": {
			leave: `SELECT pg_advisory_lock_shared(43)`,
			check: `SELECT count(*) WHERE pg_advisory_unlock_shared(43)`,
		},
		// The driver's own statement cache would name and keep every query
		// text on the server; the check's count would then include itself.
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

// A contest that permits writing never shares a connection. What a write can
// leave on a session — a temporary table, committed with it — is not something
// a reset is trusted to find, so those queries are served as before: a
// connection each, closed after.
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

// A connection whose query did not finish cleanly is closed, never handed to
// the next query. An abandoned query may have a cancel still on its way to the
// server, which would land on whatever that backend runs next, and a failed
// one leaves a session in a state nothing here inspects.
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

// An idle connection goes away on its own, so a participant who stopped asking
// does not keep a backend — or keep the reclaim sweep's plain DROP DATABASE,
// which refuses while anything is connected, from ever succeeding.
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

	// Without FORCE, exactly as the reclaim sweep drops.
	if _, err := gamedbtest.Admin(t).Exec(t.Context(), `DROP DATABASE `+sqlpolicy.QuoteIdentifier(database)); err != nil {
		t.Fatalf("a plain DROP DATABASE after the idle timeout: %v", err)
	}
}

// The runner holds no more game-cluster backends than it may run queries at
// once, kept connections included. The cluster's memory is sized for
// QUERY_CONCURRENT backends at the per-process cap (config.Runner), and a
// kept backend can be holding what its last query grew to; so a read that
// needs a new connection while the runner is at its bound closes the least
// recently used kept one first.
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

// The same bound with participants asking at once, which is the case it exists
// for: sampled from the server's side for the whole run, the runner never holds
// more participant backends than it may run queries.
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

// The pool refuses to open a connection past its bound when there is no idle
// one to close, rather than open it anyway. The gate makes that unreachable
// through Run, so the pool is driven directly here; what is proved is that a
// broken gate shows up as a refusal and not as a cluster holding more backends
// than its memory is sized for.
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

	// Handed back clean, the connection is kept idle, and an idle one is what
	// may be closed to make room.
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

// A write to a database whose read connection is being kept closes the kept
// one first. A write never takes a kept connection (it runs as another role),
// and an instance allows two connections, one of which the schema panel may be
// borrowing: the runner holding a second of its own there would turn that
// ordinary overlap into a refusal.
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

// Closing the runner closes what it kept.
func TestClosingTheRunnerClosesItsIdleConnections(t *testing.T) {
	runner, database := setupWith(t, unlimited(), anything{})

	backend(t, runner, database)
	runner.Close()

	if n := eventually(t, 0, database); n != 0 {
		t.Fatalf("a closed runner still holds %d connections", n)
	}
}
