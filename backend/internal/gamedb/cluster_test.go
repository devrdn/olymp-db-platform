package gamedb_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
)

// Running it twice must be as good as running it once: it is applied on every
// deploy, and a cluster that is already prepared is the normal case.
func TestPreparingTheClusterIsIdempotent(t *testing.T) {
	requireCluster(t)

	if err := gamedb.PrepareCluster(t.Context(), admin(t), gamedb.Roles{
		ReaderPassword: gamedbtest.ReaderPassword,
		WriterPassword: gamedbtest.WriterPassword,
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
	conn := connectAs(t, roleReader, testReaderPassword, scratchDatabase(t))

	for setting, want := range map[string]string{
		"default_transaction_read_only":       "on",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "5s",
		"work_mem":                            "16MB",
		"temp_file_limit":                     "64MB",
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
	conn := connectAs(t, roleReader, testReaderPassword, scratchDatabase(t))

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
				ReaderPassword: gamedbtest.ReaderPassword,
				WriterPassword: gamedbtest.WriterPassword,
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
