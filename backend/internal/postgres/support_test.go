package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errRollback ends a test transaction. Tests roll back rather than clean up,
// because cleanup leaks whenever a test fails.
var errRollback = errors.New("rolling back the test transaction")

var testPool *pgxpool.Pool

// TestMain connects through storagetest to the test database named by
// CORE_DB_DSN. Without it the database tests skip and the pure ones still
// run; CI sets the variable.
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
// Repositories pick it up through storage.QuerierFrom, as in production.
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

// txNow reads now() through the test transaction: its start time, which
// column defaults and repository clock comparisons use. The pool is another
// session with a different now().
func txNow(t *testing.T, ctx context.Context) time.Time {
	t.Helper()

	var now time.Time
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("read the database clock: %v", err)
	}
	return now
}

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

// contestRow commits a user and a contest and deletes both in t.Cleanup, for
// tests that cannot use withTx because ClaimBuild's SKIP LOCKED sees only
// committed rows. The contest's cascade removes game_templates and
// game_table_data.
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

// countingQuerier counts round trips; a batch counts as one.
type countingQuerier struct {
	storage.Querier
	trips *int
}

func (q countingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	*q.trips++
	return q.Querier.Query(ctx, sql, args...)
}

func (q countingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	*q.trips++
	return q.Querier.QueryRow(ctx, sql, args...)
}

func (q countingQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	*q.trips++
	return q.Querier.Exec(ctx, sql, args...)
}

func (q countingQuerier) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	*q.trips++
	return q.Querier.SendBatch(ctx, b)
}

// moveStatus moves a contest from the status it is read in to status to, as
// a caller deciding against that read would.
func moveStatus(t *testing.T, ctx context.Context, id uuid.UUID, to string) {
	t.Helper()
	repo := NewContests(testPool)
	read, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if _, err := repo.SetStatus(ctx, read, to); err != nil {
		t.Fatalf("SetStatus(%s → %s) = %v", read.Status, to, err)
	}
}

// gameStores is every store a contest's game keeps, on the test database,
// with builds as the build state.
func gameStores(builds *GameInstances) provisioning.GameStores {
	return provisioning.GameStores{Builds: builds, Dumps: NewGameUploads(testPool), TableData: NewGameTableData(testPool)}
}

// raceOnTwoConnections runs hold in a transaction it keeps open, then wait in
// a transaction on another connection. Once that connection is seen waiting
// on a lock while running a statement containing waitsIn, hold's transaction
// commits, and wait's answer is returned. The wait is read from
// pg_stat_activity for wait's own backend, so a statement that never blocked
// fails the test instead of passing on timing.
func raceOnTwoConnections(t *testing.T, hold, wait func(context.Context) error, waitsIn string) error {
	t.Helper()
	uow := storage.NewUnitOfWork(testPool)

	held, holdDone := make(chan error, 1), make(chan error, 1)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce) // a failed wait must not leave hold's transaction open
	go func() {
		holdDone <- uow.Do(context.Background(), func(ctx context.Context) error {
			err := hold(ctx)
			held <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	if err := <-held; err != nil {
		t.Fatalf("the operation holding the row = %v", err)
	}

	pid, waitDone := make(chan int32, 1), make(chan error, 1)
	go func() {
		waitDone <- uow.Do(context.Background(), func(ctx context.Context) error {
			var backend int32
			if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backend); err != nil {
				pid <- 0
				return err
			}
			pid <- backend
			return wait(ctx)
		})
	}()
	backend := <-pid
	if backend == 0 {
		t.Fatalf("the waiting operation could not start: %v", <-waitDone)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		var waitType, query string
		if err := testPool.QueryRow(context.Background(), `
			SELECT coalesce(wait_event_type, ''), query FROM pg_stat_activity WHERE pid = $1`, backend).
			Scan(&waitType, &query); err != nil {
			t.Fatalf("read the waiting connection's activity: %v", err)
		}
		if waitType == "Lock" && strings.Contains(query, waitsIn) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second operation never waited on a lock in a statement with %q; last seen %q running %q",
				waitsIn, waitType, query)
		}
		time.Sleep(5 * time.Millisecond)
	}

	releaseOnce()
	if err := <-holdDone; err != nil {
		t.Fatalf("commit the operation holding the row: %v", err)
	}
	return <-waitDone
}
