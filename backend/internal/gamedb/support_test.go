package gamedb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests connect to a real PostgreSQL and, for most of them, as the very
// role a participant's query runs under. That is the point: every guarantee
// this package makes is a guarantee about what the database refuses, and a
// database's refusals cannot be asserted from Go — they have to be provoked.
//
// Without GAME_DB_DSN the package's tests skip rather than fail, the same way
// internal/postgres does: a developer with no cluster to hand can still run
// `make test`. `make test-game` is what makes sure they actually run.
var (
	adminPool *pgxpool.Pool
	adminDSN  string
)

const (
	testReaderPassword = "reader-under-test"
	testWriterPassword = "writer-under-test"
)

func TestMain(m *testing.M) {
	adminDSN = os.Getenv("GAME_DB_DSN")
	if adminDSN == "" {
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open the game cluster: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach the game cluster: %v\n", err)
		os.Exit(1)
	}
	adminPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// requireCluster skips when there is no game cluster, and otherwise leaves the
// two participant roles prepared.
func requireCluster(t *testing.T) {
	t.Helper()

	if adminPool == nil {
		t.Skip("GAME_DB_DSN is not set; run `make test-game`")
	}
	if err := PrepareCluster(t.Context(), adminPool, Roles{
		ReaderPassword: testReaderPassword,
		WriterPassword: testWriterPassword,
	}); err != nil {
		t.Fatalf("preparing the cluster: %v", err)
	}
}

// scratchDatabase creates a hardened database and drops it afterwards.
//
// A real database rather than a transaction, because what is under test is
// what CREATE DATABASE and the catalog ACLs do, neither of which a rolled-back
// transaction can show.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	requireCluster(t)

	name := fmt.Sprintf("gamedb_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(t.Context(), `CREATE DATABASE `+quoteIdentifier(name)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) so a test that left a connection open does not leave a
		// database behind for every future run to trip over.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdentifier(name)+` WITH (FORCE)`); err != nil {
			t.Logf("could not drop %s: %v", name, err)
		}
	})

	// Hardened over a connection that is closed again immediately. A template
	// with a live connection cannot be copied — section 4.2 calls this the
	// template discipline — so a helper that left one open would make every
	// test built on it fail for a reason unrelated to what it tests.
	inside, err := pgx.Connect(t.Context(), dsnFor(t, adminUser(t), adminPassword(t), name))
	if err != nil {
		t.Fatalf("connecting to %s: %v", name, err)
	}
	err = HardenDatabase(t.Context(), inside)
	_ = inside.Close(t.Context())
	if err != nil {
		t.Fatalf("hardening %s: %v", name, err)
	}
	return name
}

// asOwner runs statements in a database and closes the connection again.
func asOwner(t *testing.T, database string, statements ...string) {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsnFor(t, adminUser(t), adminPassword(t), database))
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

// connectAs opens one connection, closed when the test ends.
func connectAs(t *testing.T, user, password, database string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsnFor(t, user, password, database))
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

// tryConnectAs reports the error instead of failing, for the tests whose
// subject is that a connection is refused.
func tryConnectAs(t *testing.T, user, password, database string) error {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsnFor(t, user, password, database))
	if err == nil {
		_ = conn.Close(t.Context())
	}
	return err
}

func dsnFor(t *testing.T, user, password, database string) string {
	t.Helper()

	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	parsed.User = url.UserPassword(user, password)
	parsed.Path = "/" + database
	return parsed.String()
}

func adminUser(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	return parsed.User.Username()
}

func adminPassword(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	password, _ := parsed.User.Password()
	return password
}

// refused runs the statement as the reader and returns the error it produced,
// failing the test if the database allowed it.
func refused(t *testing.T, conn *pgx.Conn, sql string) error {
	t.Helper()

	_, err := conn.Exec(t.Context(), sql)
	if err == nil {
		t.Fatalf("the database allowed: %s", sql)
	}
	return err
}
