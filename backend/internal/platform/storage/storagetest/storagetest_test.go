package storagetest

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCheckName(t *testing.T) {
	for name, want := range map[string]bool{
		"dbcontest_core_test": true,
		"dbcontest_game_test": true,
		"x_test":              true,
		"dbcontest_core":      false, // the product's own, the accident this exists for
		"dbcontest_game":      false,
		"postgres":            false,
		"_test":               false, // says nothing about whose tests
		"dbcontest_test_core": false,
		"dbcontest_core_TEST": false, // PostgreSQL names are case-sensitive, and so is this
		"":                    false, // no name is not a test database's name
	} {
		err := CheckName(name)
		if got := err == nil; got != want {
			t.Errorf("CheckName(%q) accepted = %t, want %t (err %v)", name, got, want, err)
		}
		if err != nil && !errors.Is(err, ErrNotATestDatabase) {
			t.Errorf("CheckName(%q) = %v, want it to wrap ErrNotATestDatabase", name, err)
		}
	}
}

// The refusal, proved on a real server the way a mistaken DSN would meet it:
// same host, same credentials, a database that is not a test database. The
// maintenance database "postgres" is the one every cluster has, so this needs
// nothing created for it — and it is also the database a careless DSN with the
// name trimmed off lands in.
//
// The database is swapped in configure, which runs before the guard is
// installed: whatever a caller does to the config, it cannot get past the
// guard by doing it.
func TestOpenRefusesADatabaseThatIsNotATestDatabase(t *testing.T) {
	dsn := os.Getenv(CoreDSNVar)
	if dsn == "" {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}

	pool, err := Open(t.Context(), dsn, func(cfg *pgxpool.Config) {
		cfg.ConnConfig.Database = "postgres"
	})
	if pool != nil {
		pool.Close()
	}
	if !errors.Is(err, ErrNotATestDatabase) {
		t.Fatalf("Open() on the maintenance database = %v, want ErrNotATestDatabase", err)
	}
}

// And the other side: what CORE_DB_DSN names in a test run is accepted, and
// the pool really is on it.
func TestOpenCoreAcceptsTheTestDatabase(t *testing.T) {
	pool, err := OpenCore(t.Context(), nil)
	if err != nil {
		t.Fatalf("OpenCore() = %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	defer pool.Close()

	var name string
	if err := pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("current_database(): %v", err)
	}
	if CheckName(name) != nil {
		t.Fatalf("the pool is on %q, which is not a test database", name)
	}
}

// OpenCore with nothing configured is a nil pool and no error — the signal
// every package's TestMain skips on.
func TestOpenCoreWithoutADSNOpensNothing(t *testing.T) {
	t.Setenv(CoreDSNVar, "")
	pool, err := OpenCore(t.Context(), nil)
	if pool != nil || err != nil {
		t.Fatalf("OpenCore() = %v, %v; want nil, nil", pool, err)
	}
}

// A hook the caller installs in configure is kept — Protect wraps it rather
// than replacing it — and runs only once the guard has accepted the
// connection, so a refused database never reaches it.
func TestProtectKeepsTheCallersHookAndRunsTheGuardFirst(t *testing.T) {
	dsn := os.Getenv(CoreDSNVar)
	if dsn == "" {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}

	// Atomic: the pool may open a connection, and so run the hook, on a
	// goroutine of its own.
	var calls atomic.Int32
	withHook := func(database string) func(*pgxpool.Config) {
		return func(cfg *pgxpool.Config) {
			if database != "" {
				cfg.ConnConfig.Database = database
			}
			cfg.AfterConnect = func(context.Context, *pgx.Conn) error { calls.Add(1); return nil }
		}
	}

	pool, err := Open(t.Context(), dsn, withHook(""))
	if err != nil {
		t.Fatalf("Open() on the test database = %v", err)
	}
	pool.Close()
	if calls.Load() == 0 {
		t.Fatal("Protect dropped the hook the caller had installed")
	}

	calls.Store(0)
	if pool, err := Open(t.Context(), dsn, withHook("postgres")); err == nil {
		pool.Close()
		t.Fatal("Open() accepted the maintenance database")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the caller's hook ran %d time(s) on a connection the guard refused", n)
	}
}
