// Package storagetest is the one door a test uses into a real PostgreSQL, and
// the door refuses any database that is not a test database.
//
// It exists because the repository tests used to connect to whatever
// CORE_DB_DSN named, and `make test-db` named the database `make run` serves
// the product from. Every run left a few hundred fixture accounts behind in
// it, and an earlier run marked the developer's real game databases as
// dropped. Asking each test to clean up after itself had already been tried
// and had already failed; this package makes the mistake impossible instead:
// a connection that lands anywhere but a test database is closed before a
// single statement of the test reaches it.
//
// # What a test database is
//
// A database whose name, as the server itself reports it through
// current_database(), ends in "_test" — dbcontest_core_test locally,
// whatever CI names its own. The name is asked of the server rather than read
// out of the DSN, so a keyword/value DSN, a database taken from PGDATABASE, a
// multi-host fallback or a pooler alias cannot slip past the check: whichever
// database a connection actually reached is the one judged.
//
// A naming rule is a guard against mistakes, not against intent. It turns a
// mistyped or copied DSN — the product's own `.../dbcontest_core` — into a
// loud failure instead of a quiet write, which is the accident that happened.
// Somebody who names a real database "..._test" has told the tests it is
// theirs; that is a decision this package cannot second-guess and does not
// try to.
//
// # What it does not do
//
// It does not create, migrate or reset the test database: that is a step
// taken once per run, before any test binary starts (cmd/testdb and the
// Makefile), because `go test` runs packages in parallel and three binaries
// racing to drop and recreate one database would destroy each other's work.
// Nor does it isolate one test from another inside that database — the
// rolled-back transactions the packages already use do that.
//
// The game cluster uses the same rule through the same Protect, applied in
// internal/gamedb/gamedbtest to the cluster's maintenance database.
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Suffix is what the name of every test database ends in.
const Suffix = "_test"

// CoreDSNVar names the environment variable a test run's core database
// arrives in. It is the same variable the product reads, deliberately: a
// shell that exports the product's DSN and then runs `go test ./...` is
// exactly the accident the guard exists for, and it has to meet the guard
// rather than a differently named variable nobody set.
const CoreDSNVar = "CORE_DB_DSN"

// ErrNotATestDatabase is the refusal: the connection reached a database whose
// name does not say it belongs to the tests.
var ErrNotATestDatabase = errors.New("refusing to run tests against a database that is not a test database")

// CheckName reports whether name is a test database's name.
//
// A name that is only the suffix is refused too: "_test" on its own says
// nothing about whose tests, and nothing a deployment would choose.
func CheckName(name string) error {
	if len(name) > len(Suffix) && strings.HasSuffix(name, Suffix) {
		return nil
	}
	return fmt.Errorf("%w: %q does not end in %q. Point the tests at a database of their own "+
		"(`make test-db` creates one) — never at the one the product runs from",
		ErrNotATestDatabase, name, Suffix)
}

// Guard asks the server which database conn is connected to, and refuses
// anything that is not a test database.
func Guard(ctx context.Context, conn *pgx.Conn) error {
	var name string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return fmt.Errorf("asking the server which database this is: %w", err)
	}
	return CheckName(name)
}

// Protect makes every connection cfg's pool opens pass Guard before the pool
// hands it out.
//
// Per connection rather than once per pool, because a pool opens connections
// for as long as it lives and a multi-host DSN can land each of them on a
// different server; the check costs one round trip per physical connection,
// which a test run does not notice. A hook the caller had already installed
// still runs, after the guard.
func Protect(cfg *pgxpool.Config) {
	previous := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := Guard(ctx, conn); err != nil {
			return err
		}
		if previous != nil {
			return previous(ctx, conn)
		}
		return nil
	}
}

// OpenCore opens a pool on the core test database named by CORE_DB_DSN.
//
// With the variable unset it returns a nil pool and no error, and the caller
// skips: a developer with no database to hand can still run `go test ./...`.
// With it set, the pool is verified before it is returned — reachable, and a
// test database — so a wrong DSN stops the package with the reason rather than
// failing its first test with a symptom.
//
// configure, when given, adjusts the pool before it connects (a query tracer,
// say); the guard is installed after it, so nothing configure does can remove
// it.
func OpenCore(ctx context.Context, configure func(*pgxpool.Config)) (*pgxpool.Pool, error) {
	dsn := os.Getenv(CoreDSNVar)
	if dsn == "" {
		return nil, nil
	}
	return Open(ctx, dsn, configure)
}

// Open is OpenCore for a DSN the caller already has.
func Open(ctx context.Context, dsn string, configure func(*pgxpool.Config)) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// The DSN itself stays out of the message: it carries a password, and
		// test output is pasted into chats and pull requests.
		return nil, errors.New("the test database DSN is not a valid connection string")
	}
	if configure != nil {
		configure(cfg)
	}
	Protect(cfg)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the test database: %w", err)
	}

	// Ping takes a connection from the pool, which is what runs the guard:
	// a refused database fails here, before any test has used the pool.
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching the test database: %w", err)
	}
	return pool, nil
}
