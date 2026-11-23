package queryproxy_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/monitor"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryproxy"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryrunner"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests assemble the façade against the real repositories
// (postgres.NewRegistrations, postgres.NewContests), because the fakes hide
// what the schema does, such as a NULL started_at (CLAUDE.md rule 10). Games,
// the database pool and the Query Runner stay fake. Without CORE_DB_DSN they
// skip; `make test-db` runs them.

// integrationPool opens the test database named by CORE_DB_DSN, or skips.
// storagetest refuses a non-test database, which matters here: these fixtures
// are committed, not rolled back.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := storagetest.OpenCore(context.Background(), nil)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run this test against a real database")
	}
	t.Cleanup(pool.Close)
	return pool
}

func makeIntegrationUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	created, err := postgres.NewUsers(pool).Create(ctx, users.User{
		Login: login, FullName: login, Status: users.StatusActive, PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("create user %q: %v", login, err)
	}
	return created.ID
}

// makeRunningIndividualContest inserts a running individual-timing contest
// with direct SQL, skipping the lifecycle the contests service enforces.
// startsAt is a parameter because the gate compares it against the test's
// pinned clock, not the database's now().
func makeRunningIndividualContest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, author uuid.UUID, durationMin int, startsAt, endsAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO contests (created_by, status, timing, duration_min, starts_at, ends_at)
		VALUES ($1, 'running', 'individual', $2, $3, $4)
		RETURNING id`, author, durationMin, startsAt, endsAt).Scan(&id)
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	return id
}

// The first query of a never-started individual participant succeeds and
// starts their clock; a query past started_at + duration_min is refused while
// the contest itself is still running.
func TestAnIndividualParticipantCanQueryOnceTheirFirstActionStartsTheClockAndCannotAfterTheirDeadline(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	const durationMin = 10
	// A pinned clock, which starts_at must also be set against, so the gate's
	// window check uses the same time as the deadline check.
	clock := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	contestID := makeRunningIndividualContest(t, ctx, pool, author, durationMin, clock.Add(-time.Hour), clock.Add(24*time.Hour))
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})

	registrations := postgres.NewRegistrations(pool)
	if _, err := registrations.Add(ctx, contestID, student); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	service := newService(
		registrations,
		postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"},
		&runner{result: &queryrunner.Result{Columns: []string{"a"}}},
		fiveSecondGate,
		audit.New(postgres.NewAuditSink(pool)), storage.NewUnitOfWork(pool), at(func() time.Time { return clock }),
	)

	cmd := queryproxy.Command{ContestID: contestID.String(), UserID: student, SQL: `SELECT 1`, RequestID: uuid.New()}

	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("the participant's first query: %v", err)
	}

	stored, err := registrations.ByUser(ctx, contestID, student)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.StartedAt == nil || !stored.StartedAt.Equal(clock) {
		t.Fatalf("StartedAt = %v, want %v — the first query must have started the clock", stored.StartedAt, clock)
	}
	if stored.Status != contests.RegistrationActive {
		t.Fatalf("status = %q, want %q", stored.Status, contests.RegistrationActive)
	}

	clock = clock.Add(5 * time.Minute)
	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("a query within the participant's own window: %v", err)
	}

	// Past started_at + duration_min while the contest is still running.
	clock = clock.Add(durationMin * time.Minute)
	if _, err := service.Run(ctx, cmd); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("a query past the participant's own deadline: error = %v, want ErrDeadlinePassed", err)
	}
}

func makeRunningFixedContest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, author uuid.UUID, endsAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO contests (created_by, status, timing, ends_at)
		VALUES ($1, 'running', 'fixed', $2)
		RETURNING id`, author, endsAt).Scan(&id)
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	return id
}

// The fact that closes the console is decided in SQL (postgres.Answerable) and
// applied in Go (Run), so it is tested across that boundary (CLAUDE.md
// rule 11).
func TestTheConsoleClosesOnceNothingIsAnswerableAgainstTheRealSchema(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	contestID := makeRunningFixedContest(t, ctx, pool, author, time.Now().Add(24*time.Hour))
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})

	// Direct SQL: postgres.Questions.Create requires the authoring transaction.
	max := 1
	var questionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO questions (contest_id, ord, kind, points, max_attempts, is_visible)
		VALUES ($1, 1, 'text', 5, $2, true)
		RETURNING id`, contestID, max).Scan(&questionID); err != nil {
		t.Fatalf("create question: %v", err)
	}

	registrations := postgres.NewRegistrations(pool)
	registration, err := registrations.Add(ctx, contestID, student)
	if err != nil {
		t.Fatalf("Add() = %v", err)
	}

	service := newService(
		registrations,
		postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"},
		&runner{result: &queryrunner.Result{Columns: []string{"a"}}},
		fiveSecondGate,
		audit.New(postgres.NewAuditSink(pool)), storage.NewUnitOfWork(pool),
	).WithAnswerable(postgres.NewAnswerable(pool))

	cmd := queryproxy.Command{ContestID: contestID.String(), UserID: student, SQL: `SELECT 1`, RequestID: uuid.New()}

	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("a query while the contest's only question is still open: %v", err)
	}

	if _, err := postgres.NewSubmissions(pool).Insert(ctx, contests.SubmissionRequest{
		RegistrationID: registration.ID, QuestionID: questionID, Value: "wrong", IsCorrect: false,
		Points: 5, MaxAttempts: &max, Deadline: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}

	if _, err := service.Run(ctx, cmd); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("a query with every question closed: error = %v, want ErrNothingLeftToAnswer", err)
	}

	// Only the console closes: the play screen's reads still admit them, which
	// is why this is not ErrParticipantFinished.
	reads := participation.New(participation.Config{
		Lookup: registrations, People: registrations, Gate: fiveSecondGate,
		Audit: audit.New(postgres.NewAuditSink(pool)), UnitOfWork: storage.NewUnitOfWork(pool),
	})
	if _, err := reads.Admit(ctx, participation.NewReads(0), participation.Request{Contest: contestID.String(), Account: student}); err != nil {
		t.Fatalf("Admit() = %v, want nil — only the console closes", err)
	}
}

// countingTracer counts every statement pgx sends over a real connection.
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

// panicCluster fails the test if Run reaches the game cluster: every fixture
// participant already has a current game_instances row, so Ensure must answer
// from repo.Of alone.
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

// noOpExecutor stands in for the Query Runner, which is a gRPC call and never
// a core database round trip.
type noOpExecutor struct{}

func (noOpExecutor) Run(context.Context, queryrunner.Request, queryrunner.Origin) (*queryrunner.Result, error) {
	return &queryrunner.Result{Columns: []string{"a"}}, nil
}

// roundTrips measures the steady state: a fixed-timing, read-only contest with
// a ready template and a participant with a current copy, so neither Quota nor
// the game cluster adds a round trip. The participant is not started: the
// warm-up call records the start, so the measured call shows that a
// participant already started costs no Start (CLAUDE.md rule 6).
type roundTrips struct {
	tracer    *countingTracer
	admitter  *participation.Admitter
	service   *queryproxy.Service
	contestID uuid.UUID
	student   uuid.UUID
}

func roundTripFixture(t *testing.T) roundTrips {
	t.Helper()
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

	// Unique names: the fixture is committed and game database names are unique
	// across the installation.
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, version, init_script, status)
		VALUES ($1, $2, 1, 'SELECT 1', 'ready')`, contestID, "game_tpl_roundtrip_"+suffix); err != nil {
		t.Fatalf("insert ready template: %v", err)
	}

	registrations := postgres.NewRegistrations(pool)
	registration, err := registrations.Add(ctx, contestID, student)
	if err != nil {
		t.Fatalf("Add() = %v", err)
	}
	games := postgres.NewGameInstances(pool)
	if err := games.Assign(ctx, contestID, registration.ID, "game_db_roundtrip_"+suffix, 1); err != nil {
		t.Fatalf("Assign() = %v", err)
	}

	databases := provisioning.New(games, panicCluster{t: t}, audit.New(postgres.NewAuditSink(pool)), storage.NewUnitOfWork(pool))
	admitter := participation.New(participation.Config{
		Lookup: registrations, People: registrations, Gate: fiveSecondGate,
		Audit: audit.New(postgres.NewAuditSink(pool)), UnitOfWork: storage.NewUnitOfWork(pool),
	})
	service := queryproxy.New(admitter, registrations, databases, noOpExecutor{}).
		WithAnswerable(postgres.NewAnswerable(pool)).
		WithSchemas(&schemas{})
	return roundTrips{tracer: tracer, admitter: admitter, service: service, contestID: contestID, student: student}
}

// measure runs call once to warm the connection, then counts the statements a
// second identical call sends.
func (r roundTrips) measure(t *testing.T, call func() error) int {
	t.Helper()
	if err := call(); err != nil {
		t.Fatalf("warm-up call = %v", err)
	}
	r.tracer.reset()
	if err := call(); err != nil {
		t.Fatalf("measured call = %v", err)
	}
	return r.tracer.get()
}

// A steady-state query costs the core database two statements: the combined
// lookup and AnswerableLeft.
func TestRunsCoreRoundTripsAreMeasured(t *testing.T) {
	r := roundTripFixture(t)
	cmd := queryproxy.Command{ContestID: r.contestID.String(), UserID: r.student, SQL: `SELECT 1`, RequestID: uuid.New()}

	got := r.measure(t, func() error {
		_, err := r.service.Run(context.Background(), cmd)
		return err
	})
	if got != 2 {
		t.Errorf("core round trips = %d, want 2 (one combined lookup, one AnswerableLeft)", got)
	}
}

// Every participant-facing read is admitted through participation, so its
// single statement is paid on each one, autosaves and signals included.
func TestAdmissionCostsOneCoreRoundTrip(t *testing.T) {
	r := roundTripFixture(t)

	got := r.measure(t, func() error {
		_, err := r.admitter.Admit(context.Background(), participation.NewReads(0),
			participation.Request{Contest: r.contestID.String(), Account: r.student})
		return err
	})
	if got != 1 {
		t.Errorf("core round trips = %d, want 1 (participant and contest together)", got)
	}
}

// The schema panel costs two statements: admission, then the combined lookup.
// The start recorded by the warm-up costs the measured read nothing.
func TestTheSchemaReadCostsTwoCoreRoundTrips(t *testing.T) {
	r := roundTripFixture(t)

	got := r.measure(t, func() error {
		admission, err := r.admitter.Admit(context.Background(), participation.NewReads(0),
			participation.Request{Contest: r.contestID.String(), Account: r.student})
		if err != nil {
			return err
		}
		if _, err = r.service.Schema(context.Background(), admission.Contest, admission.Participant); err != nil {
			return err
		}
		return admission.Start(context.Background())
	})
	if got != 2 {
		t.Errorf("core round trips = %d, want 2 (admission, then the combined lookup)", got)
	}
}

// answeringRunner answers without a game database.
type answeringRunner struct{}

func (answeringRunner) Run(context.Context, queryrunner.Request) (*queryrunner.Result, error) {
	return &queryrunner.Result{Columns: []string{"a"}}, nil
}

// The client address crosses the façade's command, the journal wrapper and
// the real query_log insert to reach its row (CLAUDE.md rule 11); the same
// insert writes the fingerprint.
func TestAQueryRowRecordsTheClientAddressAndTheFingerprint(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	contestID := makeRunningFixedContest(t, ctx, pool, author, time.Now().Add(24*time.Hour))
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})

	registrations := postgres.NewRegistrations(pool)
	registration, err := registrations.Add(ctx, contestID, student)
	if err != nil {
		t.Fatalf("Add() = %v", err)
	}
	journalled := queryrunner.NewJournalled(answeringRunner{}, postgres.NewQueryLog(pool),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	service := newService(
		registrations,
		postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"},
		journalled,
		fiveSecondGate,
		audit.New(postgres.NewAuditSink(pool)), storage.NewUnitOfWork(pool),
	)

	address := netip.MustParseAddr("203.0.113.9")
	if _, err := service.Run(ctx, queryproxy.Command{
		ContestID: contestID.String(), UserID: student, SQL: "SELECT kind, found_at\n\tFROM Evidence\n WHERE room = 'library' ORDER BY found_at",
		Address: address, RequestID: uuid.New(),
	}); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	var ip *netip.Addr
	var fingerprint *int64
	if err := pool.QueryRow(ctx,
		`SELECT ip, sql_fingerprint FROM query_log WHERE registration_id = $1`, registration.ID).
		Scan(&ip, &fingerprint); err != nil {
		t.Fatalf("read the journal row: %v", err)
	}
	if ip == nil || *ip != address {
		t.Fatalf("ip = %v, want %v", ip, address)
	}
	if fingerprint == nil || *fingerprint != monitor.Fingerprint("select kind, found_at from evidence where room = 'library' order by found_at") {
		t.Fatalf("sql_fingerprint = %v, want the fingerprint of the normalised statement", fingerprint)
	}
}
