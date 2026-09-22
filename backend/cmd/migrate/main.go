// Command migrate applies the core database schema migrations.
//
// Migrations run as a deliberate step (a compose job or a CI stage), never as
// a side effect of the API starting: several API replicas starting at once
// must not race to alter the schema.
//
// Usage:
//
//	migrate up            apply all pending migrations
//	migrate down          roll back the most recent migration
//	migrate version       print the current schema version
//	migrate force <n>     clear a dirty state after a failed migration
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/devrdn/db-contest/backend/migrations"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	// Registers the pgx driver under the name "pgx" for database/sql, which
	// golang-migrate needs.
	"github.com/jackc/pgx/v5/stdlib"
)

// The timeouts a migration runs under.
//
// statement_timeout is cleared: the deployment sets one for the API's own
// pool, and an index build is long by nature — killed halfway it leaves an
// invalid index and a version that did not move.
//
// lock_timeout is deliberately NOT set here, although waiting for a lock is
// the dangerous thing a migration does: DDL queued for an ACCESS EXCLUSIVE
// lock makes every request needing the same table queue behind it. It is not
// set here because it cannot be a property of the connection. `CREATE INDEX
// CONCURRENTLY` waits for every transaction older than itself by taking that
// transaction's virtualxid lock, and those waits go through the lock manager
// too: a connection-wide lock_timeout aborts the build as soon as any
// ordinary transaction of this service outlives it — an export holds one for
// up to a minute by design — and leaves an invalid index and a dirty
// schema_migrations behind, with the API's own start gated on the migration
// having succeeded.
//
// So the timeout belongs to the migration that wants it: a file that takes a
// heavy lock opens with `SET lock_timeout` of its own, inside the implicit
// transaction the file already runs in, and a file that builds an index
// concurrently holds one statement and no timeout at all. migrations_test.go
// holds both halves of that convention.

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: migrate <up|down|version|force N>")
	}

	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		return errors.New("CORE_DB_DSN is not set")
	}

	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer closeFn()

	switch args[0] {
	case "up":
		return report(m.Up(), "schema is up to date")
	case "down":
		return report(m.Steps(-1), "rolled back one migration")
	case "version":
		version, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			fmt.Println("no migrations applied yet")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("version %d (dirty: %t)\n", version, dirty)
		return nil
	case "force":
		if len(args) < 2 {
			return errors.New("force requires a version number")
		}
		version, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid version %q: %w", args[1], err)
		}
		return report(m.Force(version), "version forced")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// migrationConfig is the connection a migration runs on: whatever it does is
// not cut short by a timeout the deployment set for serving traffic.
func migrationConfig(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the core database address: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["statement_timeout"] = "0"
	return cfg, nil
}

func newMigrator(dsn string) (*migrate.Migrate, func(), error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	cfg, err := migrationConfig(dsn)
	if err != nil {
		return nil, nil, err
	}
	// The registration is process-global and keyed by a name the library
	// invents; the cleanup below gives it back, so a caller that builds
	// several migrators in one process (the tests do) does not grow the
	// library's map with an entry per call.
	name := stdlib.RegisterConnConfig(cfg)
	db, err := sql.Open("pgx", name)
	if err != nil {
		stdlib.UnregisterConnConfig(name)
		return nil, nil, fmt.Errorf("open core database: %w", err)
	}

	closeDB := func() {
		_ = db.Close()
		stdlib.UnregisterConnConfig(name)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("initialise migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("initialise migrator: %w", err)
	}

	return m, closeDB, nil
}

// report treats "nothing to do" as success: re-running the job must be safe.
func report(err error, okMessage string) error {
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	fmt.Println(okMessage)
	return nil
}
