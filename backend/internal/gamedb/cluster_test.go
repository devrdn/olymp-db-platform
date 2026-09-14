package gamedb_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// Running it twice must be as good as running it once: it is applied on every
// deploy, and a cluster that is already prepared is the normal case.
func TestPreparingTheClusterIsIdempotent(t *testing.T) {
	requireCluster(t)

	if err := gamedb.PrepareCluster(t.Context(), admin(t), gamedb.Roles{
		ReaderPassword: gamedbtest.ReaderPassword(t),
		WriterPassword: gamedbtest.WriterPassword(t),
		AuthorPassword: gamedbtest.AuthorPassword(t),
	}); err != nil {
		t.Fatalf("a second run failed: %v", err)
	}
}

// The settings are defaults for a session, and a session can change most of
// them. They are worth setting anyway — they bound the ordinary case — but the
// tests below distinguish carefully between what they bound and what they
// guarantee.
func TestTheReaderStartsWithTheIntendedSettings(t *testing.T) {
	// A game database, not the maintenance one: the reader is deliberately
	// barred from that, which the last test in database_test.go proves.
	conn := connectAs(t, roleReader, testReaderPassword(t), scratchDatabase(t))

	for setting, want := range map[string]string{
		"default_transaction_read_only":       "on",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "5s",
		"work_mem":                            "16MB",
		"temp_file_limit":                     "64MB",
		// Capped so a participant query occupies at most a leader plus one
		// worker process, each under the per-process memory cap — the count the
		// deployment's memory arithmetic is sized for (config.Runner). A
		// participant cannot raise it: SET is not a statement the validator
		// admits.
		"max_parallel_workers_per_gather": "1",
		// Bounds how long a backend keeps running after its client has gone,
		// so an abandoned query does not hold a memory cap's worth of the
		// cluster until statement_timeout.
		"client_connection_check_interval": "250ms",
	} {
		t.Run(setting, func(t *testing.T) {
			var got string
			if err := conn.QueryRow(t.Context(), "SHOW "+setting).Scan(&got); err != nil {
				t.Fatalf("reading %s: %v", setting, err)
			}
			if got != want {
				t.Fatalf("%s = %q, want %q", setting, got, want)
			}
		})
	}
}

// This test asserts a limitation rather than a guarantee, deliberately.
//
// statement_timeout and default_transaction_read_only are USERSET: the session
// can change them, and `SET` is itself SQL. So neither is a boundary against a
// participant whose SQL reached the database unchecked — they are defaults
// that bound the ordinary case. What actually stops a write is the absence of
// a GRANT, which the next test proves, and what actually bounds time is the
// Query Runner's own deadline on the connection, which no SQL can reach
// (section 4.4).
//
// It is written down as a test because the alternative is that somebody later
// reads `ALTER ROLE ... SET statement_timeout` and believes it is a limit.
func TestWhatTheRoleSettingsDoNotGuarantee(t *testing.T) {
	conn := connectAs(t, roleReader, testReaderPassword(t), scratchDatabase(t))

	for _, statement := range []string{
		"SET statement_timeout = 0",
		"SET default_transaction_read_only = off",
	} {
		if _, err := conn.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%q now fails (%v) — if this became a real limit, "+
				"update the comment above and section 4.3 of the architecture", statement, err)
		}
	}

	// temp_file_limit is the one that is not USERSET, so it is a real bound on
	// disk spilled by a single query.
	if _, err := conn.Exec(t.Context(), "SET temp_file_limit = -1"); err == nil {
		t.Fatal("temp_file_limit became settable by the session; the disk bound is gone")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("temp_file_limit refused for an unexpected reason: %v", err)
	}
}

func TestTheRolesExistAndCannotBecomeMore(t *testing.T) {
	requireCluster(t)

	var superuser, createdb, createrole, canLogin bool
	err := admin(t).QueryRow(t.Context(),
		`SELECT rolsuper, rolcreatedb, rolcreaterole, rolcanlogin
		 FROM pg_roles WHERE rolname = $1`, roleReader).
		Scan(&superuser, &createdb, &createrole, &canLogin)
	if err != nil {
		t.Fatalf("reading the reader role: %v", err)
	}

	if !canLogin {
		t.Fatal("the reader cannot log in; the Query Runner connects as it")
	}
	for name, granted := range map[string]bool{
		"superuser": superuser, "createdb": createdb, "createrole": createrole,
	} {
		if granted {
			t.Errorf("the reader role holds %s", name)
		}
	}
}

// Idempotent has to mean concurrent too.
//
// Two replicas of the job, a retry after a timeout, or simply `docker compose
// up` next to somebody's manual run: two of these overlap and PostgreSQL
// answers `tuple concurrently updated` — SQLSTATE XX000, an internal error
// indistinguishable from a real fault. It was found by Go running two test
// binaries against the same cluster at once, which is exactly the shape of the
// production case.
func TestPreparingTheClusterSurvivesConcurrentRuns(t *testing.T) {
	requireCluster(t)
	pool := admin(t)

	const runs = 8
	failures := make(chan error, runs)

	var wg sync.WaitGroup
	for range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- gamedb.PrepareCluster(t.Context(), pool, gamedb.Roles{
				ReaderPassword: gamedbtest.ReaderPassword(t),
				WriterPassword: gamedbtest.WriterPassword(t),
				AuthorPassword: gamedbtest.AuthorPassword(t),
			})
		}()
	}
	wg.Wait()
	close(failures)

	for err := range failures {
		if err != nil {
			t.Fatalf("a concurrent run failed: %v", err)
		}
	}
}

// `make test-game` must not lock out `make runner`.
//
// The tests and a development stack share one cluster and one pair of roles,
// and every test run prepares those roles again. It used to prepare them with
// passwords of the harness's own, so the first `make test-game` after a stack
// was started took the Query Runner's credentials away from it — silently,
// until a participant ran a query and the console answered with the cluster's
// address. This asserts the property directly: after a test run has prepared
// the cluster, the credentials the deployment uses still open a connection.
func TestPreparingTheClusterForTestsLeavesTheDeploymentsCredentialsWorking(t *testing.T) {
	// Scratch prepares the cluster on the way, so this is the state a test run
	// leaves a developer's machine in.
	database := scratchDatabase(t)

	for _, role := range []struct{ name, variable string }{
		{gamedb.RoleReader, "GAME_READER_PASSWORD"},
		{gamedb.RoleWriter, "GAME_WRITER_PASSWORD"},
		// The Core API's own credential on this cluster, and the same
		// argument: a test run that changed it would leave a running `make
		// run` unable to build a game, and nothing would say so until an
		// organiser pressed the button.
		{gamedb.RoleAuthor, "GAME_AUTHOR_PASSWORD"},
	} {
		t.Run(role.name, func(t *testing.T) {
			password := os.Getenv(role.variable)
			if password == "" {
				t.Fatalf("%s is not set, so what a running stack authenticates with is unknown; run `make test-game`", role.variable)
			}
			if err := tryConnectAs(t, role.name, password, database); err != nil {
				t.Fatalf("a test run took %s away from the running stack: %v", role.name, err)
			}
		})
	}
}

// The role an organiser's game script runs as, checked the same way the
// participants' are: by reading what the cluster says it is rather than what
// the deploy meant.
//
// Every attribute here is one the script would otherwise be able to use to get
// out. SUPERUSER is the whole boundary; CREATEROLE is a superuser one ALTER
// ROLE later; CREATEDB makes it the owner of databases it creates, and an
// owner may DROP DATABASE. The role memberships are the other half: three
// predefined roles hand out exactly the powers the COPY tests provoke, so a
// membership in any of them would make those refusals lapse silently.
func TestTheAuthorRoleCannotBecomeMoreThanAnAuthor(t *testing.T) {
	requireCluster(t)

	var superuser, createdb, createrole, replication, bypassRLS, canLogin bool
	err := admin(t).QueryRow(t.Context(),
		`SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolcanlogin
		 FROM pg_roles WHERE rolname = $1`, gamedb.RoleAuthor).
		Scan(&superuser, &createdb, &createrole, &replication, &bypassRLS, &canLogin)
	if err != nil {
		t.Fatalf("reading the author role: %v", err)
	}

	if !canLogin {
		t.Fatal("the author role cannot log in; the Core API connects as it to run a game script")
	}
	for name, granted := range map[string]bool{
		"superuser": superuser, "createdb": createdb, "createrole": createrole,
		"replication": replication, "bypassrls": bypassRLS,
	} {
		if granted {
			t.Errorf("the author role holds %s", name)
		}
	}

	for _, predefined := range []string{
		"pg_read_server_files",
		"pg_write_server_files",
		"pg_execute_server_program",
		"pg_read_all_data",
		"pg_write_all_data",
	} {
		t.Run(predefined, func(t *testing.T) {
			var member bool
			if err := admin(t).QueryRow(t.Context(),
				`SELECT pg_has_role($1, $2, 'USAGE')`, gamedb.RoleAuthor, predefined).Scan(&member); err != nil {
				t.Fatalf("asking about %s: %v", predefined, err)
			}
			if member {
				t.Fatalf("the author role has the privileges of %s", predefined)
			}
		})
	}
}

// The author role's sessions are deliberately not bounded the way a
// participant's are.
//
// A game script is one long run of DDL and INSERTs against a database nobody
// is looking at; the five-second statement_timeout that is right for a
// stranger's SELECT would fail every olympiad whose data takes longer than
// that to load. What bounds a build instead is the deadline its caller puts on
// the context, which is on the connection and cannot be `SET` away.
//
// Asserted rather than left implicit, because "0" here is a decision and an
// inherited 5s would look exactly like one until an author's build started
// timing out for no reason anybody could see.
func TestTheAuthorStartsWithNoStatementTimeoutOfItsOwn(t *testing.T) {
	conn := connectAs(t, gamedb.RoleAuthor, gamedbtest.AuthorPassword(t), scratchDatabase(t))

	var timeout string
	if err := conn.QueryRow(t.Context(), `SHOW statement_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("reading statement_timeout: %v", err)
	}
	if timeout != "0" {
		t.Fatalf("statement_timeout = %q for the author role, want 0: a build is bounded by "+
			"the caller's deadline, not by a figure sized for a participant's query", timeout)
	}
}

// A role membership somebody granted is taken back by the next deploy, the
// same way an attribute somebody granted is.
//
// prepareRole's ALTER names every attribute a role must *not* have precisely
// so a cluster edited by hand during a contest is put back. A membership is
// the same kind of edit and is invisible to that statement: the five
// predefined roles PostgreSQL ships hand out exactly the powers a game script
// must not have, and one GRANT makes every refusal in
// TestAHostileGameScriptIsRefusedTheThingsOnlyASuperuserCanDo lapse while the
// role still reads as NOSUPERUSER NOCREATEDB NOCREATEROLE.
//
// It is not hypothetical. A cluster that ran a game script before this
// boundary existed ran it as a superuser, so `GRANT pg_read_server_files TO
// game_author` inside one is a leftover an upgrade has to remove — and until
// it does, the new role is the old hole under a new name.
func TestPreparingTheClusterTakesBackARoleMembershipSomebodyGranted(t *testing.T) {
	pool := admin(t)

	for _, role := range []string{gamedb.RoleAuthor, gamedb.RoleReader, gamedb.RoleWriter} {
		t.Run(role, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(),
				`GRANT pg_read_server_files TO `+role); err != nil {
				t.Fatalf("granting the membership to provoke the case: %v", err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `REVOKE pg_read_server_files FROM `+role)
			})

			if err := gamedb.PrepareCluster(t.Context(), pool, gamedb.Roles{
				ReaderPassword: gamedbtest.ReaderPassword(t),
				WriterPassword: gamedbtest.WriterPassword(t),
				AuthorPassword: gamedbtest.AuthorPassword(t),
			}); err != nil {
				t.Fatalf("preparing the cluster: %v", err)
			}

			var member bool
			if err := pool.QueryRow(t.Context(),
				`SELECT pg_has_role($1, 'pg_read_server_files', 'USAGE')`, role).Scan(&member); err != nil {
				t.Fatalf("asking about the membership: %v", err)
			}
			if member {
				t.Fatalf("%s still has the privileges of pg_read_server_files after a deploy", role)
			}
		})
	}
}

// A backend whose client disappears without a goodbye — the Query Runner killed
// mid-query, a dropped network — receives no Terminate and no CancelRequest.
// Nothing then tells it the client is gone: with statement_timeout off it would
// run for as long as its query does, holding a memory cap's worth of the
// cluster. client_connection_check_interval on the participant roles is what
// ends it: the backend polls its socket and gives up once the client is gone.
//
// The test reproduces exactly that, and has to go around the driver to do it:
// pgconn, on any socket error in the middle of a query, sends a CancelRequest
// before closing (its asyncClose), so closing the socket underneath pgconn is a
// cancelled query, not a vanished client. Instead the connection is set up as
// the reader with statement_timeout off, then hijacked — pgconn lets go of it —
// and the long count(*) is written on the raw socket as a plain Query message.
// The socket is then closed; no Terminate and no cancel can be sent, because
// nothing that would send them still holds the connection. The backend must be
// gone within a second. With the interval at 0 it is still running when the
// test gives up (and is terminated by the cleanup).
func TestAnAbruptlyAbandonedBackendStopsWithinTheCheckInterval(t *testing.T) {
	database := scratchDatabase(t)
	pool := admin(t)

	conn, err := pgconn.Connect(t.Context(), gamedbtest.DSN(t, roleReader, testReaderPassword(t), database))
	if err != nil {
		t.Fatalf("connecting as the reader: %v", err)
	}
	pid := conn.PID()
	// Whatever the outcome, never leave a backend with no statement_timeout
	// running on the test cluster.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, pid)
	})

	if _, err := conn.Exec(t.Context(), `SET statement_timeout = 0`).ReadAll(); err != nil {
		t.Fatalf("turning statement_timeout off for the session: %v", err)
	}

	raw, err := conn.Hijack()
	if err != nil {
		t.Fatalf("hijacking the connection: %v", err)
	}
	raw.Frontend.Send(&pgproto3.Query{
		String: `SELECT count(*) FROM generate_series(1, 100000) a, generate_series(1, 100000) b`,
	})
	if err := raw.Frontend.Flush(); err != nil {
		t.Fatalf("sending the long query: %v", err)
	}

	running := func() bool {
		var active bool
		if err := pool.QueryRow(t.Context(),
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			                 WHERE pid = $1 AND state = 'active' AND query LIKE '%generate_series%')`,
			pid).Scan(&active); err != nil {
			t.Fatalf("reading pg_stat_activity: %v", err)
		}
		return active
	}
	// Executing, not merely received: seen active on the long query, and still
	// active a moment later.
	for deadline := time.Now().Add(5 * time.Second); !running(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("backend %d never started the long query", pid)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if !running() {
		t.Fatalf("backend %d stopped before the client went away", pid)
	}

	// The client vanishes.
	if err := raw.Conn.Close(); err != nil {
		t.Fatalf("closing the raw socket: %v", err)
	}
	dropped := time.Now()

	for time.Since(dropped) < 3*time.Second {
		if !running() {
			if took := time.Since(dropped); took > time.Second {
				t.Fatalf("backend %d ran %s after its client vanished, want under 1s", pid, took)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("backend %d still running 3s after its client vanished with no Terminate and no cancel: "+
		"client_connection_check_interval is not ending abandoned backends", pid)
}

// The Query Runner sizes the game cluster's memory from constants that restate
// settings owned here and on the pg-game command: how many build sessions the
// game_author role may hold, how many parallel workers and autovacuum workers
// the cluster runs. They live in the platform layer, which must not import this
// package, so nothing ties them at compile time; this reads each setting back
// from the prepared cluster and compares. A change on either side without the
// other fails here instead of silently leaving the memory arithmetic wrong.
func TestTheMemoryArithmeticMatchesTheCluster(t *testing.T) {
	pool := admin(t)

	var authorLimit int
	if err := pool.QueryRow(t.Context(),
		`SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, gamedb.RoleAuthor).Scan(&authorLimit); err != nil {
		t.Fatalf("reading %s's connection limit: %v", gamedb.RoleAuthor, err)
	}
	if authorLimit != config.MaxBuildSessions {
		t.Errorf("%s CONNECTION LIMIT = %d, but config.MaxBuildSessions = %d",
			gamedb.RoleAuthor, authorLimit, config.MaxBuildSessions)
	}

	for setting, want := range map[string]int{
		"max_parallel_workers":   config.MaxParallelWorkers,
		"autovacuum_max_workers": config.AutovacuumWorkers,
	} {
		var raw string
		if err := pool.QueryRow(t.Context(), "SHOW "+setting).Scan(&raw); err != nil {
			t.Fatalf("SHOW %s: %v", setting, err)
		}
		got, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("SHOW %s = %q is not a number", setting, raw)
		}
		if got != want {
			t.Errorf("%s = %d on the cluster, but the memory arithmetic assumes %d", setting, got, want)
		}
	}
}
