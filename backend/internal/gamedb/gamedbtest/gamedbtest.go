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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The passwords the two participant roles are given while under test.
//
// Fixed rather than generated so that a failing run can be reproduced by hand
// against the same cluster. They are only ever set on a developer's local
// pg-game container by the tests themselves; a real installation's passwords
// come from the environment and never from here. This package is imported by
// test binaries alone.
//
// #nosec G101 -- test fixtures for a local container, not a credential.
const (
	ReaderPassword = "reader-under-test"
	WriterPassword = "writer-under-test"
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

// Admin returns a pool as the provisioning role, with the cluster prepared.
func Admin(t *testing.T) *pgxpool.Pool {
	t.Helper()

	once.Do(connect)
	if open != nil {
		t.Fatal(open)
	}
	if pool == nil {
		t.Skip("GAME_DB_DSN is not set; run `make test-game`")
	}

	if err := gamedb.PrepareCluster(t.Context(), pool, gamedb.Roles{
		ReaderPassword: ReaderPassword,
		WriterPassword: WriterPassword,
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
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+gamedb.QuoteIdentifier(name)); err != nil {
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
	_, _ = pool.Exec(ctx, `DROP DATABASE IF EXISTS `+gamedb.QuoteIdentifier(name)+` WITH (FORCE)`)
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
