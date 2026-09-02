package provisioning_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These run against the real repository, not a stand-in for it. A fake
// repository would agree with the service about claiming, versions and the
// composite reference, and every one of those is a property of the SQL — the
// two would drift the first time one of them was edited.
//
// The cluster, on the other hand, is faked: what it does is create and drop
// databases, which internal/gamedb tests against a real one. Here it is only
// asked what it was told to do.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open the core database: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach the core database: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// cluster records what it was asked to do, and can be told to refuse.
type cluster struct {
	mu      sync.Mutex
	made    []string
	dropped []string
	fail    error
}

func (c *cluster) CreateInstance(_ context.Context, _, instance string, _ sqlpolicy.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fail != nil {
		return c.fail
	}
	c.made = append(c.made, instance)
	return nil
}

func (c *cluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dropped = append(c.dropped, name)
	return nil
}

func (c *cluster) counts() (made, dropped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.made), len(c.dropped)
}

// contestFor sets up a contest and the given number of registrations, removed
// again when the test ends.
func contestFor(t *testing.T, registrations int) (provisioning.Contest, []uuid.UUID) {
	t.Helper()

	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	ctx := t.Context()

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

	var people []uuid.UUID
	for range registrations {
		var user, registration uuid.UUID
		if err := testPool.QueryRow(ctx,
			`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Player', 'x')
			 RETURNING id`, "prov-player-"+uuid.NewString()[:8]).Scan(&user); err != nil {
			t.Fatalf("create player: %v", err)
		}
		if err := testPool.QueryRow(ctx,
			`INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2) RETURNING id`,
			id, user).Scan(&registration); err != nil {
			t.Fatalf("create registration: %v", err)
		}
		people = append(people, registration)
	}

	return provisioning.Contest{
		ID: id, Template: "game_tpl_test", Version: 1, Policy: sqlpolicy.ReadOnly(),
	}, people
}
