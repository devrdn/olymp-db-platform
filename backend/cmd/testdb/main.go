// Command testdb drops and recreates, empty, the test database CORE_DB_DSN
// names; `make test-db` then runs `migrate up`. A clean slate makes leftovers
// of earlier runs irrelevant and applies migrations from zero each time. It is
// a command, not a test step, because test binaries run in parallel.
//
// It refuses, before connecting, any database whose name does not end in
// "_test", since it issues DROP DATABASE.
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

// maintenanceDatabase is where DROP and CREATE are issued from, since
// PostgreSQL cannot drop the database a session is connected to.
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
	// FORCE: a killed earlier run may have left a connection open.
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
// a config for the same server's maintenance database and the name to
// recreate. The check is on the DSN's name, since the database may not exist
// yet; a DSN with no name is refused.
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
