package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
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
//
// Through storagetest, which refuses any database that is not a test
// database: `make test-db` used to point this at the one `make run` serves
// the product from, and every run left its fixtures there.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := storagetest.OpenCore(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot use the test database: %v\n", err)
		os.Exit(1)
	}
	if pool == nil {
		os.Exit(m.Run())
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
// makeRegistration enrols a user in a contest, which is what the query log is
// keyed by: the same person in two contests is two participants.
func makeRegistration(t *testing.T, ctx context.Context, contest, user uuid.UUID) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2) RETURNING id`,
		contest, user).Scan(&id)
	if err != nil {
		t.Fatalf("create registration: %v", err)
	}
	return id
}

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

// contestRow inserts a user and a contest committed for real, with a
// t.Cleanup that deletes both — provisioning's own contestFor's shape
// (support_test.go:491), for a test that cannot run inside withTx because it
// needs its own commits visible to a second claim (ClaimBuild's SKIP LOCKED
// only ever sees committed rows). The contest's cascade
// (000003_game_and_registrations.up.sql) takes game_templates and
// game_table_data with it, so nothing else needs its own cleanup.
func contestRow(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}

	var author uuid.UUID
	err := testPool.QueryRow(ctx,
		`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Author', 'x')
		 RETURNING id`, "prov-author-"+uuid.NewString()[:8]).Scan(&author)
	if err != nil {
		t.Fatalf("create author: %v", err)
	}

	var id uuid.UUID
	if err := testPool.QueryRow(ctx,
		`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author).Scan(&id); err != nil {
		t.Fatalf("create contest: %v", err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
		_, _ = testPool.Exec(clean, `DELETE FROM users WHERE id = $1`, author)
	})

	return id
}
