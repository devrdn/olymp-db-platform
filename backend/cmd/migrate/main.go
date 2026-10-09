// Command migrate applies the core database schema migrations, as a separate
// step so API replicas starting together never race to alter the schema.
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

	// Registers the pgx driver for database/sql, which golang-migrate needs.
	"github.com/jackc/pgx/v5/stdlib"
)

// The timeouts a migration runs under.
//
// statement_timeout is cleared: an index build killed halfway leaves an
// invalid index and a version that did not move.
//
// lock_timeout is not set on the connection: CREATE INDEX CONCURRENTLY waits
// on the virtualxid locks of older transactions (an export holds one for up
// to a minute), so a connection-wide timeout would abort it and leave a dirty
// schema. Instead a file taking a heavy lock sets its own lock_timeout, and a
// concurrent index build has none; migrations_test.go enforces both.

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

// migrationConfig is the connection a migration runs on, without the
// deployment's serving timeouts.
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
	// The registration is process-global; the cleanup releases it so
	// repeated migrators (in tests) do not grow the library's map.
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
