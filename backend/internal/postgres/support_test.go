package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errRollback ends a test transaction. Every integration test below runs
// inside one and leaves nothing behind: the alternative — creating rows and
// deleting them afterwards — leaks whenever a test fails, and a failed test is
// exactly when the next run needs a clean database.
var errRollback = errors.New("rolling back the test transaction")

// testPool is opened once for the whole package; opening a pool per test would
// spend more time connecting than querying.
var testPool *pgxpool.Pool

// TestMain connects to the database named by CORE_DB_DSN, if there is one.
//
// Without it the package's tests still run: what they skip is the part that
// needs a server, and what stays is the constraint mapping, which is pure. A
// developer with no PostgreSQL to hand should not be stopped from running the
// suite — but nor should the SQL go unverified, so CI sets the variable.
func TestMain(m *testing.M) {
	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open the test database: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach the test database: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// withTx runs body inside a transaction that is always rolled back.
//
// The repositories pick the transaction up from the context through
// storage.QuerierFrom, which is the same path a real request takes, so what is
// exercised here is the production code path and not a test-only variant.
func withTx(t *testing.T, body func(ctx context.Context)) {
	t.Helper()
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}

	err := storage.NewUnitOfWork(testPool).Do(context.Background(), func(ctx context.Context) error {
		body(ctx)
		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("test transaction failed: %v", err)
	}
}

// makeUser stores an account the contest fixtures can hang off.
func makeUser(t *testing.T, ctx context.Context, login string) users.User {
	t.Helper()

	created, err := NewUsers(testPool).Create(ctx, users.User{
		Login:        login,
		FullName:     login,
		Status:       users.StatusActive,
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("create user %q: %v", login, err)
	}
	return created
}

// makeContest stores a draft contest owned by author.
func makeContest(t *testing.T, ctx context.Context, author uuid.UUID) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author).Scan(&id)
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	return id
}
