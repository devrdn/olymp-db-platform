package queryproxy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// countingTracer counts every statement pgx sends over the wire, through the
// same pgx.QueryTracer hook storagetest.Open's own `configure` callback
// exists to install. It is the measurement P-H3's own task brief asks for:
// not a count of Go-level calls into a fake, but of round trips a real
// connection actually made.
type countingTracer struct {
	mu    sync.Mutex
	count int
}

func (c *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	return ctx
}

func (c *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *countingTracer) reset() {
	c.mu.Lock()
	c.count = 0
	c.mu.Unlock()
}

func (c *countingTracer) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// panicCluster is a provisioning.Cluster that fails the test if Run ever
// reaches it: the fixture below gives every participant an existing, current
// game_instances row, so Ensure must answer from repo.Of alone and never ask
// the game cluster for anything.
type panicCluster struct{ t *testing.T }

func (p panicCluster) CreateInstance(context.Context, string, string, sqlpolicy.Policy) error {
	p.t.Fatal("the game cluster was asked to create an instance; the fixture's own copy should have been reused")
	return nil
}
func (p panicCluster) Drop(context.Context, string) error {
	p.t.Fatal("the game cluster was asked to drop a database")
	return nil
}
func (p panicCluster) DatabaseSize(context.Context, string) (int64, error) {
	p.t.Fatal("the game cluster was asked for a database's size")
	return 0, nil
}
func (p panicCluster) ClusterBytes(context.Context) (int64, error) {
	p.t.Fatal("the game cluster was asked for its total size")
	return 0, nil
}
func (p panicCluster) DatabaseSizes(context.Context, []string) (map[string]int64, error) {
	p.t.Fatal("the game cluster was asked for several databases' sizes")
	return nil, nil
}
func (p panicCluster) DropIdle(context.Context, string) (bool, error) {
	p.t.Fatal("the game cluster was asked to drop an idle database")
	return false, nil
}

// noOpExecutor stands in for the Query Runner: what happens once the SQL
// actually leaves this process is a gRPC call, never a core database round
// trip, so it is faked out rather than measured.
type noOpExecutor struct{}

func (noOpExecutor) Run(context.Context, queryrunner.Request, uuid.UUID) (*queryrunner.Result, error) {
	return &queryrunner.Result{Columns: []string{"a"}}, nil
}

// TestRunsCoreRoundTripsAreMeasured is the measurement the task brief asks
// for: how many statements one steady-state Run call sends to the core
// database, counted with a real pgx.QueryTracer against dbcontest_core_test
// rather than assumed from reading the code. The scenario is the common
// case an olympiad spends almost all of its queries in — a fixed-timing,
// read-only contest, a participant who has already started and already has
// a current game database — so neither Start nor Quota adds a round trip of
// its own, and what is left is exactly the lookup(s) this task changed.
//
// Run this same test — after copying it, since the type it measures did not
// exist yet — against the commit before this task (3d7e881) in a throwaway
// worktree to get the "before" figure the report cites; see
// task-6a-report.md's "Fix round 1" section for both numbers.
func TestRunsCoreRoundTripsAreMeasured(t *testing.T) {
	ctx := context.Background()
	tracer := &countingTracer{}
	pool, err := storagetest.OpenCore(ctx, func(cfg *pgxpool.Config) {
		cfg.ConnConfig.Tracer = tracer
	})
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run this test against a real database")
	}
	t.Cleanup(pool.Close)

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	farFuture := time.Now().Add(24 * time.Hour)
	contestID := makeRunningFixedContest(t, ctx, pool, author, farFuture)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})

	// A ready template — read-only, so Quota is never asked for — and a
	// participant already holding a current copy of it, so Ensure answers
	// from repo.Of alone.
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, version, init_script, status)
		VALUES ($1, 'game_tpl_roundtrip', 1, 'SELECT 1', 'ready')`, contestID); err != nil {
		t.Fatalf("insert ready template: %v", err)
	}

	registrations := postgres.NewRegistrations(pool)
	registration, err := registrations.Add(ctx, contestID, student)
	if err != nil {
		t.Fatalf("Add() = %v", err)
	}
	if _, err := registrations.Start(ctx, registration.ID, time.Now()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	games := postgres.NewGameInstances(pool)
	if err := games.Assign(ctx, contestID, registration.ID, "game_db_roundtrip", 1); err != nil {
		t.Fatalf("Assign() = %v", err)
	}

	databases := provisioning.New(games, panicCluster{t: t})
	service := queryproxy.New(registrations, postgres.NewContests(pool), games, databases, noOpExecutor{}).
		WithAnswerable(postgres.NewAnswerable(pool)).
		WithLookup(registrations)

	cmd := queryproxy.Command{ContestID: contestID, UserID: student, SQL: `SELECT 1`, RequestID: uuid.New()}

	// One warm-up call so connection setup (any statement pgx itself issues
	// while establishing the session) does not inflate the count, then reset
	// the tracer and measure the next, otherwise identical, call.
	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("warm-up Run() = %v", err)
	}
	tracer.reset()

	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("measured Run() = %v", err)
	}

	got := tracer.get()
	t.Logf("core round trips for one steady-state Run() with the merged lookup: %d", got)
	// Three is the claim this task's report makes: one combined lookup
	// (participant + contest + game), one AnswerableLeft, one Ensure (→
	// repo.Of). A regression that reopens any of the two collapsed round
	// trips changes this number, which is exactly what should fail here.
	if got != 3 {
		t.Errorf("core round trips = %d, want 3 — see task-6a-report.md's Fix round 1 for what each one is", got)
	}
}
