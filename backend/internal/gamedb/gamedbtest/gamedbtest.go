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
// # Which cluster
//
// Never the one the product runs on. On a game cluster what the tests act on
// is the cluster itself: CREATE DATABASE and DROP DATABASE name cluster-wide
// objects, and preparing the cluster rewrites game_reader, game_writer and
// game_author, which are cluster-wide too. Pointed at the development
// cluster, test runs did both to the installation — took a running Query
// Runner's credentials away mid-session, so the next participant's query
// failed to connect, and left databases of their own behind in the cluster
// the product's disk quota is measured on.
//
// So the test targets start a cluster of their own (pg-game-test in
// deploy/docker-compose.dev.yml, recreated for every run), CI starts one per
// job, and connect refuses any cluster whose maintenance database — the one
// GAME_DB_DSN names, as the server reports it — does not end in "_test". That
// is the core database's rule (internal/platform/storage/storagetest), applied
// here to the database that stands for the cluster.
//
// # Why it does not invent credentials
//
// The roles are the product's roles, by name and by the code that prepares
// them (gamedb.PrepareCluster), because what these tests prove is what
// PostgreSQL refuses to *those* roles as the deploy prepares them — a copy
// under another name would be a copy of the code under test rather than the
// thing itself. Their passwords are the caller's to state, in
// GAME_READER_PASSWORD, GAME_WRITER_PASSWORD and GAME_AUTHOR_PASSWORD: the
// Makefile passes deploy/.env's, CI passes literals. Missing, they are a hard
// failure and never a default. A harness that picked its own is how a test
// run once locked the Query Runner out of a shared cluster; the test cluster
// is what makes that impossible now, and refusing to guess keeps it harmless
// if a DSN ever points somewhere the guard has been talked into accepting.
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
//
// Through storagetest.Open, so the pool refuses a cluster that is not a test
// cluster before any helper has prepared a role or created a database on it.
// The refusal lands in open and fails every test that asks for the cluster.
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

// Configured reports whether this run was given a game cluster at all. A test
// that has to decide before it builds any fixture asks this; everything else
// simply calls Admin or Scratch, which skip on their own.
func Configured() bool { return os.Getenv("GAME_DB_DSN") != "" }

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

	// The caller's passwords, never ones made up here: see the package
	// comment.
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
//
// On the cluster connect accepted, and only on it: a refused or missing
// cluster stops the test here rather than handing it an address to connect to
// on its own, outside the guard.
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
