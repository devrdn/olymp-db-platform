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

// How long a migration may wait for a lock before it gives up.
//
// A migration runs against a database that is serving. DDL waits behind
// whatever is already reading, and while it waits every request that needs
// the same table queues behind it — so a migration that "only takes a moment"
// stops the service for as long as one long-running reader holds on. Five
// seconds is longer than any query this API is allowed to run
// (storage.coreStatementTimeout is ten, and only a maintenance pool goes
// above it), so a migration that cannot start in that time is waiting on
// something it should not be waiting on: it fails, leaves the schema where it
// was, and the service keeps serving.
const migrationLockTimeout = "5s"

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

// migrationConfig is the connection a migration runs on: it refuses to wait
// for a lock, and it is not cut short once it has one.
//
// statement_timeout is cleared rather than inherited: the deployment sets one
// for the API's own pool, and an index build is long by nature — being killed
// halfway through leaves an invalid index and a schema version that did not
// move.
func migrationConfig(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the core database address: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["lock_timeout"] = migrationLockTimeout
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
	db, err := sql.Open("pgx", stdlib.RegisterConnConfig(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("open core database: %w", err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("initialise migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("initialise migrator: %w", err)
	}

	return m, func() { _ = db.Close() }, nil
}

// report treats "nothing to do" as success: re-running the job must be safe.
func report(err error, okMessage string) error {
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	fmt.Println(okMessage)
	return nil
}
