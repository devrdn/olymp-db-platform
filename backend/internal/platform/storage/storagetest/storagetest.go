// Package storagetest is the one way a test connects to a real PostgreSQL,
// and it refuses any database that is not a test database: one whose name,
// as current_database() reports it, ends in "_test". Asking the server
// rather than parsing the DSN covers PGDATABASE, multi-host DSNs and pooler
// aliases. The rule guards against a mistyped or copied DSN, not intent.
//
// It does not create, migrate or reset the test database (cmd/testdb does,
// once per run, since `go test` runs packages in parallel), and it does not
// isolate tests from each other. internal/gamedb/gamedbtest applies the same
// Protect to the game cluster.
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

// CoreDSNVar names the variable holding the core test database's DSN. It is
// the one the product reads, so a shell that exported the product's DSN meets
// the guard.
const CoreDSNVar = "CORE_DB_DSN"

// ErrNotATestDatabase means the connection reached a database whose name does
// not end in Suffix.
var ErrNotATestDatabase = errors.New("refusing to run tests against a database that is not a test database")

// CheckName reports whether name is a test database's name. The bare suffix
// is refused.
func CheckName(name string) error {
	if len(name) > len(Suffix) && strings.HasSuffix(name, Suffix) {
		return nil
	}
	return fmt.Errorf("%w: %q does not end in %q. The tests run only against databases of their own "+
		"— `make test-db` and `make test-game-build` provide them — never against the ones the product runs from",
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
// hands it out. The check is per connection because a multi-host DSN can land
// each one on a different server. An existing AfterConnect hook runs after
// the guard.
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

// OpenCore opens a pool on the core test database named by CORE_DB_DSN and
// verifies it is reachable and a test database. With the variable unset it
// returns a nil pool and no error, and the caller skips.
//
// configure, when given, adjusts the pool before it connects; the guard is
// installed after it, so configure cannot remove it.
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
		// The DSN stays out of the message: it carries a password.
		return nil, errors.New("the test database DSN is not a valid connection string")
	}
	if configure != nil {
		configure(cfg)
	}
	Protect(cfg)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the database: %w", err)
	}

	// Ping takes a connection, which runs the guard before any test uses the
	// pool.
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting: %w", err)
	}
	return pool, nil
}
