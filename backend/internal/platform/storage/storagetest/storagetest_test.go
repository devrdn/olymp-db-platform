package storagetest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The guard only protects the tests that go through it. A test that reads
// CORE_DB_DSN or GAME_DB_DSN itself and opens its own pool walks straight
// past it — which is how every core test connected until this package
// existed, and what the next test written in a hurry would do by habit. So
// this reads the test code of the whole module and fails on any read of
// either variable outside the two doors: this package for the core database,
// gamedbtest for the game cluster.
//
// It looks for the literal read, the form that habit produces. It is a
// tripwire, not a proof: a test that goes out of its way to build the name
// can still get past it, and review is what catches that.
func TestNoTestReadsADatabaseDSNPastTheGuard(t *testing.T) {
	root := moduleRoot(t)
	read := regexp.MustCompile(`(Getenv|LookupEnv)\("(CORE|GAME)_DB_DSN"\)`)
	doors := map[string]bool{
		"internal/platform/storage/storagetest": true,
		"internal/gamedb/gamedbtest":            true,
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(rel))

		// Test code is a _test.go file or anything in a <pkg>test helper
		// package. The product's own reads (internal/platform/config, the
		// commands under cmd/) are neither, and are not this test's business.
		testCode := strings.HasSuffix(path, "_test.go") || strings.HasSuffix(filepath.Base(filepath.Dir(path)), "test")
		if !testCode || doors[dir] {
			return nil
		}

		source, err := os.ReadFile(path) // #nosec G304 -- a file of this module, found by walking it.
		if err != nil {
			return err
		}
		if read.Match(source) {
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these read a database DSN themselves instead of going through storagetest.OpenCore "+
			"or gamedbtest, so nothing stops them connecting to the product's database:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot is the directory holding go.mod, found by walking up from this
// package — where `go test` runs it.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

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
