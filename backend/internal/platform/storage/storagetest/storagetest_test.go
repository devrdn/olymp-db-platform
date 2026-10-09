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

// The guard protects only the tests that go through it, so this fails on any
// test code that reads CORE_DB_DSN or GAME_DB_DSN outside this package and
// gamedbtest. It matches the literal read only; a test that builds the name
// can still get past it.
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

		// Test code is a _test.go file or anything in a <pkg>test package; the
		// product's own reads are neither.
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
		"dbcontest_core":      false,
		"dbcontest_game":      false,
		"postgres":            false,
		"_test":               false,
		"dbcontest_test_core": false,
		"dbcontest_core_TEST": false, // PostgreSQL names are case-sensitive
		"":                    false,
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

// The maintenance database "postgres" exists on every cluster. It is swapped
// in through configure, which runs before the guard is installed.
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

func TestOpenCoreWithoutADSNOpensNothing(t *testing.T) {
	t.Setenv(CoreDSNVar, "")
	pool, err := OpenCore(t.Context(), nil)
	if pool != nil || err != nil {
		t.Fatalf("OpenCore() = %v, %v; want nil, nil", pool, err)
	}
}

func TestProtectKeepsTheCallersHookAndRunsTheGuardFirst(t *testing.T) {
	dsn := os.Getenv(CoreDSNVar)
	if dsn == "" {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}

	// Atomic: the pool may run the hook on its own goroutine.
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
