package gamedb_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// It runs on every deploy, so a prepared cluster is the normal case.
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

func TestTheReaderStartsWithTheIntendedSettings(t *testing.T) {
	// The reader is barred from the maintenance database.
	conn := connectAs(t, roleReader, testReaderPassword(t), scratchDatabase(t))

	for setting, want := range map[string]string{
		"default_transaction_read_only":       "on",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "5s",
		"work_mem":                            "16MB",
		"temp_file_limit":                     "64MB",
		"max_parallel_workers_per_gather":     "1",
		"client_connection_check_interval":    "250ms",
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

// This asserts a limitation: statement_timeout and
// default_transaction_read_only are USERSET, so a session can `SET` them off.
// Writes are stopped by missing GRANTs and time by the Query Runner's deadline.
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

	// temp_file_limit is not USERSET, so it is a real bound.
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

// Overlapping runs must queue, not fail with `tuple concurrently updated`.
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

// Tests and a development stack share the cluster and its roles, so a test
// run must not change the passwords the running stack uses.
func TestPreparingTheClusterForTestsLeavesTheDeploymentsCredentialsWorking(t *testing.T) {
	// scratchDatabase prepares the cluster on the way.
	database := scratchDatabase(t)

	for _, role := range []struct{ name, variable string }{
		{gamedb.RoleReader, "GAME_READER_PASSWORD"},
		{gamedb.RoleWriter, "GAME_WRITER_PASSWORD"},
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

// Each attribute and predefined-role membership checked here would let a game
// script escape: CREATEROLE leads to superuser, CREATEDB to owning (and so
// dropping) databases, and the predefined roles grant what the COPY tests
// provoke.
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

// A build is bounded by its caller's deadline; an inherited 5s would fail any
// game whose data takes longer to load.
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

// A membership is invisible to prepareRole's ALTER, and one GRANT of a
// predefined role would undo the refusals in
// TestAHostileGameScriptIsRefusedTheThingsOnlyASuperuserCanDo.
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

// A backend whose client vanishes (runner killed, network dropped) gets no
// Terminate or cancel; client_connection_check_interval must end it.
//
// pgconn sends a CancelRequest when its socket fails mid-query, so the test
// hijacks the connection and writes the long query on the raw socket, then
// closes it. With statement_timeout off, only the interval can stop the
// backend.
func TestAnAbruptlyAbandonedBackendStopsWithinTheCheckInterval(t *testing.T) {
	database := scratchDatabase(t)
	pool := admin(t)

	conn, err := pgconn.Connect(t.Context(), gamedbtest.DSN(t, roleReader, testReaderPassword(t), database))
	if err != nil {
		t.Fatalf("connecting as the reader: %v", err)
	}
	pid := conn.PID()
	// Never leave a backend without statement_timeout running.
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
	// Executing, not merely received.
	for deadline := time.Now().Add(5 * time.Second); !running(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("backend %d never started the long query", pid)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if !running() {
		t.Fatalf("backend %d stopped before the client went away", pid)
	}

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
