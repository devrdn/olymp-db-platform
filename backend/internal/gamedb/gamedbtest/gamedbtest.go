// Package gamedbtest hands a test a real game cluster.
//
// It exists because two packages need the same arrangement — a prepared
// cluster, a hardened database, a connection as the participant's own role —
// and because that arrangement cannot be faked. Every guarantee either package
// makes is a guarantee about what PostgreSQL refuses, and a refusal has to be
// provoked rather than asserted.
//
// Without GAME_DB_DSN every helper skips the test rather than failing it: a
// developer with no cluster to hand can still run `make test`. `make test-game`
// is what makes sure they actually run.
//
// # Why it does not invent credentials
//
// The cluster it prepares is the same one `make dev-up` starts and the same
// pair of roles `make runner` connects as — game_reader and game_writer are
// cluster-wide, and preparing them states their passwords. So a harness with
// passwords of its own is a test run that silently takes a running Query
// Runner's credentials away from it: every query after it fails to connect,
// and the participant is the one who finds out. That is not hypothetical. It
// is where the connection failure a console once displayed came from.
//
// The roles stay shared, because what these tests prove is what PostgreSQL
// refuses to *those* roles as the deploy prepares them — a copy under another
// name would be a copy of the code under test rather than the thing itself.
// What changes is where the passwords come from: GAME_READER_PASSWORD and
// GAME_WRITER_PASSWORD, the same two variables the deployment sets, so
// preparing the cluster for a test writes back exactly what is already there.
// Missing, they are a hard failure and never a default — a harness guessing a
// password here is the whole defect.
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
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The environment the deployment's own credentials arrive in — the three
// variables deploy/.env sets, docker-compose passes to the Query Runner and
// the Core API, and `make test-game` passes to the tests.
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

// connect opens the cluster once for the whole test binary. Lazy rather than
// in a TestMain, so that a package using these helpers does not have to have
// one — and so two packages cannot disagree about how it is set up.
func connect() {
	dsn = os.Getenv("GAME_DB_DSN")
	if dsn == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		open = fmt.Errorf("opening the game cluster: %w", err)
		return
	}
	if err := p.Ping(ctx); err != nil {
		open = fmt.Errorf("reaching the game cluster: %w", err)
		return
	}
	pool = p
}

// ReaderPassword and WriterPassword are what the two participant roles
// authenticate with, for the tests that connect as one of them.
//
// The deployment's, read from the environment rather than chosen here: see the
// package comment. A test binary that has a cluster but no credentials for it
// stops rather than making some up, because making some up is what breaks the
// stack the developer is running.
func ReaderPassword(t *testing.T) string { t.Helper(); return credential(t, readerPasswordVar) }

func WriterPassword(t *testing.T) string { t.Helper(); return credential(t, writerPasswordVar) }

// AuthorPassword is what the game-script role authenticates with — the Core
// API's own credential for this cluster, read from the deployment's
// environment for the same reason the other two are.
func AuthorPassword(t *testing.T) string { t.Helper(); return credential(t, authorPasswordVar) }

func credential(t *testing.T, variable string) string {
	t.Helper()

	requireCluster(t)

	password := os.Getenv(variable)
	if password == "" {
		// Loud, and on the first test that needs it. The alternative — a
		// password of the harness's own — is silent until somebody's running
		// Query Runner cannot authenticate any more, which is a failure that
		// surfaces during a contest rather than during a test run.
		t.Fatalf("%s is not set. These tests prepare the cluster's shared %s and %s roles, "+
			"so they need the passwords the deployment already uses rather than passwords of "+
			"their own — run `make test-game`, which passes them from deploy/.env.",
			variable, gamedb.RoleReader, gamedb.RoleWriter)
	}
	return password
}

// requireCluster opens the cluster, skipping the test where there is none to
// open and failing where there is one that cannot be reached.
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

	// The deployment's own passwords, so that preparing the cluster for a test
	// writes back what a running Query Runner is already using instead of
	// locking it out.
	if err := gamedb.PrepareCluster(t.Context(), pool, gamedb.Roles{
		ReaderPassword: ReaderPassword(t),
		WriterPassword: WriterPassword(t),
		AuthorPassword: AuthorPassword(t),
	}); err != nil {
		t.Fatalf("preparing the cluster: %v", err)
	}
	return pool
}

// DSN builds a connection string for a role and a database.
func DSN(t *testing.T, user, password, database string) string {
	t.Helper()

	once.Do(connect)
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

	once.Do(connect)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	password, _ := parsed.User.Password()
	return parsed.User.Username(), password
}

// Scratch creates a hardened database and drops it when the test ends.
//
// A real database rather than a transaction: what these tests are about is
// what CREATE DATABASE and the catalog ACLs do, neither of which a rolled-back
// transaction can show. No connection is left open on it, because a database
// with a live connection cannot be used as a template.
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

// Drop removes a database, terminating whatever is still connected to it.
//
// WITH (FORCE) because a test that failed halfway is exactly the run that left
// a connection open, and a database left behind is a name every later run
// trips over.
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
