// Package gamedbtest gives tests a real, prepared game cluster, shared by
// gamedb and the Query Runner. It cannot be faked: the guarantees under test
// are what PostgreSQL refuses. Without GAME_DB_DSN every helper skips.
//
// It refuses any cluster whose maintenance database does not end in "_test",
// because the tests create databases and rewrite the cluster-wide roles. The
// roles are the product's own, and their passwords must come from
// GAME_READER_PASSWORD, GAME_WRITER_PASSWORD and GAME_AUTHOR_PASSWORD; made-up
// passwords would lock out a running stack that shares the cluster.
package gamedbtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The deployment's credentials, as deploy/.env sets them.
const (
	readerPasswordVar = "GAME_READER_PASSWORD" // #nosec G101 -- a variable's name.
	writerPasswordVar = "GAME_WRITER_PASSWORD" // #nosec G101 -- a variable's name.
	authorPasswordVar = "GAME_AUTHOR_PASSWORD" // #nosec G101 -- a variable's name.
)

var (
	once sync.Once
	pool *pgxpool.Pool
	dsn  string
	open error
)

// connect opens the cluster once per test binary, lazily so callers need no
// TestMain. storagetest.Open refuses a non-test cluster before anything is
// prepared on it; the refusal lands in open.
func connect() {
	dsn = os.Getenv("GAME_DB_DSN")
	if dsn == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := storagetest.Open(ctx, dsn, nil)
	if err != nil {
		open = fmt.Errorf("the game cluster in GAME_DB_DSN: %w", err)
		return
	}
	pool = p
}

// Configured reports whether this run was given a game cluster. Admin and
// Scratch skip on their own; this is for deciding before building a fixture.
func Configured() bool { return os.Getenv("GAME_DB_DSN") != "" }

// ReaderPassword and WriterPassword are the participant roles' deployment
// passwords, read from the environment; a missing one fails the test.
func ReaderPassword(t *testing.T) string { t.Helper(); return credential(t, readerPasswordVar) }

func WriterPassword(t *testing.T) string { t.Helper(); return credential(t, writerPasswordVar) }

// AuthorPassword is the game-script role's deployment password.
func AuthorPassword(t *testing.T) string { t.Helper(); return credential(t, authorPasswordVar) }

func credential(t *testing.T, variable string) string {
	t.Helper()

	requireCluster(t)

	password := os.Getenv(variable)
	if password == "" {
		t.Fatalf("%s is not set. These tests prepare the cluster's shared %s and %s roles, "+
			"so they need the passwords the deployment already uses rather than passwords of "+
			"their own — run `make test-game`, which passes them from deploy/.env.",
			variable, gamedb.RoleReader, gamedb.RoleWriter)
	}
	return password
}

// requireCluster skips without a configured cluster and fails if it was
// refused or unreachable.
func requireCluster(t *testing.T) {
	t.Helper()

	once.Do(connect)
	if open != nil {
		t.Fatal(open)
	}
	if pool == nil {
		t.Skip("GAME_DB_DSN is not set; run `make test-game`")
	}
}

// Admin returns a pool as the provisioning role, with the cluster prepared.
func Admin(t *testing.T) *pgxpool.Pool {
	t.Helper()

	requireCluster(t)

	if err := gamedb.PrepareCluster(t.Context(), pool, gamedb.Roles{
		ReaderPassword: ReaderPassword(t),
		WriterPassword: WriterPassword(t),
		AuthorPassword: AuthorPassword(t),
	}); err != nil {
		t.Fatalf("preparing the cluster: %v", err)
	}
	return pool
}

// DSN builds a connection string for a role and a database, only on a cluster
// connect accepted, so no test reaches a cluster outside the guard.
func DSN(t *testing.T, user, password, database string) string {
	t.Helper()

	requireCluster(t)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	parsed.User = url.UserPassword(user, password)
	parsed.Path = "/" + database
	return parsed.String()
}

// AdminCredentials returns the provisioning role's name and password.
func AdminCredentials(t *testing.T) (string, string) {
	t.Helper()

	requireCluster(t)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	password, _ := parsed.User.Password()
	return parsed.User.Username(), password
}

// Scratch creates a hardened database and drops it when the test ends. No
// connection is left open, so it can serve as a template.
func Scratch(t *testing.T) string {
	t.Helper()

	admin := Admin(t)
	name := fmt.Sprintf("gamedb_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(name)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() { Drop(name) })

	user, password := AdminCredentials(t)
	conn, err := pgx.Connect(t.Context(), DSN(t, user, password, name))
	if err != nil {
		t.Fatalf("connecting to %s: %v", name, err)
	}
	err = gamedb.HardenDatabase(t.Context(), conn)
	_ = conn.Close(t.Context())
	if err != nil {
		t.Fatalf("hardening %s: %v", name, err)
	}
	return name
}

// Drop removes a database, terminating whatever is still connected to it (a
// test that failed halfway may have left a connection open).
func Drop(name string) {
	if pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DROP DATABASE IF EXISTS `+sqlpolicy.QuoteIdentifier(name)+` WITH (FORCE)`)
}

// Run executes statements in a database and closes the connection again.
func Run(t *testing.T, database string, statements ...string) {
	t.Helper()

	user, password := AdminCredentials(t)
	conn, err := pgx.Connect(t.Context(), DSN(t, user, password, database))
	if err != nil {
		t.Fatalf("connecting to %s: %v", database, err)
	}
	defer func() { _ = conn.Close(t.Context()) }()

	for _, statement := range statements {
		if _, err := conn.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// Connect opens one connection, closed when the test ends.
func Connect(t *testing.T, user, password, database string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), DSN(t, user, password, database))
	if err != nil {
		t.Fatalf("connecting as %s to %s: %v", user, database, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(ctx)
	})
	return conn
}
