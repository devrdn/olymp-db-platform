// Command testdb recreates the core database the DB-backed tests run
// against: it drops the database CORE_DB_DSN names and creates it again,
// empty. `make test-db` runs it and then `migrate up`, so every run starts
// from a freshly migrated schema that nothing but the tests has ever touched.
//
// Why drop and recreate rather than keep the database and clean it: a clean
// slate costs about a second, and it makes whatever a previous run left behind
// — a fixture a test forgot, the prov-player accounts contestFor never
// deletes, a run killed half-way — irrelevant rather than something to hunt
// for. It also means the migrations are applied from zero on every run, which
// is the path a new installation takes.
//
// Why a command and not a step inside the test binaries: `go test` runs
// packages in parallel, and three binaries each dropping the database the
// other two are using would destroy each other's work. The reset has to
// happen once, before any of them starts.
//
// It refuses any database whose name does not end in "_test" — the same rule
// the tests connect under (internal/platform/storage/storagetest) — and it
// refuses before connecting to anything, because this is the one place in the
// test tooling that issues DROP DATABASE.
//
// Usage:
//
//	CORE_DB_DSN=postgres://.../dbcontest_core_test testdb
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/jackc/pgx/v5"
)

// maintenanceDatabase is where the DROP and the CREATE are issued from:
// PostgreSQL cannot drop the database a session is connected to, and every
// cluster initdb makes has this one.
const maintenanceDatabase = "postgres"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv(storagetest.CoreDSNVar)
	if dsn == "" {
		return errors.New(storagetest.CoreDSNVar + " is not set")
	}
	cfg, name, err := target(dsn)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to the %s database on the test server: %w", maintenanceDatabase, err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	quoted := pgx.Identifier{name}.Sanitize()
	// WITH (FORCE): the run this follows may have been killed with a
	// connection still open, and a reset that waits for it never finishes.
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("dropping %s: %w", name, err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoted); err != nil {
		return fmt.Errorf("creating %s: %w", name, err)
	}
	fmt.Printf("recreated %s, empty; run the migrations next\n", name)
	return nil
}

// target parses dsn, refuses it unless it names a test database, and returns
// a connection config for the same server's maintenance database together
// with the name of the database to recreate.
//
// The check is on the name the DSN gives, not on the server's answer as the
// tests' own guard does, because the database may not exist yet — and a
// missing name (one left to PGDATABASE or to the role's name) is refused
// rather than guessed at.
func target(dsn string) (*pgx.ConnConfig, string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Not the DSN itself: it carries a password.
		return nil, "", fmt.Errorf("%s is not a valid connection string", storagetest.CoreDSNVar)
	}
	name := cfg.Database
	if err := storagetest.CheckName(name); err != nil {
		return nil, "", fmt.Errorf("not dropping %q: %w", name, err)
	}
	cfg.Database = maintenanceDatabase
	return cfg, name, nil
}
