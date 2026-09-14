package queryproxy_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Finding 2: every other test in this package hands queryproxy.Service a
// fake for People, so the one case the deployment always hits on a fresh
// registration — StartedAt is NULL — was exactly what the fakes hid. This
// file assembles the façade against the real repositories instead
// (postgres.NewRegistrations, postgres.NewContests), which is the only way
// to prove queryproxy and the schema actually agree about what "started"
// means (CLAUDE.md rule 10). Games, the database pool and the Query Runner
// stay fake: they answer a question this test is not asking.
//
// `make test-db` is what runs this file against a real database; without
// CORE_DB_DSN it skips, same as every test in internal/postgres.

// integrationPool opens the test database named by CORE_DB_DSN, or skips.
// storagetest refuses a database that is not a test database, which matters
// here more than anywhere: the fixtures below are committed, not rolled back.
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

// makeIntegrationUser stores an account the fixtures below hang off.
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

// makeRunningIndividualContest inserts a contest already running under
// individual timing, with the exact fields contests.Deadline (and, since
// finding 1, Contest.OpenForStart) read. Direct SQL rather than the contests
// service: reaching "running" through the service means walking
// draft → published → running, which is the lifecycle package's own concern
// and only noise here.
//
// startsAt is a parameter rather than the database's own now() minus an
// interval: the test below drives queryproxy with its own pinned clock
// (WithClock), which has nothing to do with the wall-clock time this fixture
// is created at, and OpenForStart compares starts_at against that pinned
// clock, not against when the row was inserted.
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

// TestAnIndividualParticipantCanQueryOnceTheirFirstActionStartsTheClockAndCannotAfterTheirDeadline
// is the guarantee finding 2 asks for, proven against the real schema: the
// first query from an individual-timing participant who has never started
// succeeds and starts their clock, and a query after their own deadline —
// started_at + duration_min — is refused, even though the contest's own
// status and window are both still "running".
func TestAnIndividualParticipantCanQueryOnceTheirFirstActionStartsTheClockAndCannotAfterTheirDeadline(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	const durationMin = 10
	// "Now" is pinned and advanced by the test rather than raced against the
	// wall clock: Run uses the same clock for both starting the participant
	// and comparing their deadline, so the two calls below are exactly ten
	// minutes and one second apart from the façade's own point of view. The
	// contest's own starts_at has to be pinned against this same clock, not
	// the database's own now(): OpenForStart (finding 1) compares starts_at
	// to whatever clock Run is given, and a fixture stamped by the real wall
	// clock would place a fixed 2026 test date outside a window that opened
	// today.
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
	service := queryproxy.New(
		registrations,
		postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"},
		&runner{result: &queryrunner.Result{Columns: []string{"a"}}},
	).WithClock(func() time.Time { return clock })

	cmd := queryproxy.Command{ContestID: contestID, UserID: student, SQL: `SELECT 1`, RequestID: uuid.New()}

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

	// Still well inside the participant's own ten minutes: unaffected.
	clock = clock.Add(5 * time.Minute)
	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("a query within the participant's own window: %v", err)
	}

	// Past started_at + duration_min, even though the contest's own status
	// and window are both still "running" — the defect finding 1 fixed, now
	// proven against the schema rather than a fake that could not have hidden
	// it either way.
	clock = clock.Add(durationMin * time.Minute)
	if _, err := service.Run(ctx, cmd); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("a query past the participant's own deadline: error = %v, want ErrContestNotRunning", err)
	}
}

// makeRunningFixedContest inserts a contest already running under fixed
// timing, sharing one window for every participant.
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

// TestAccessAgainstTheRealSchemaAnswersTheOwnersOwnStandingCheck proves, on
// the real schema rather than a fake, the exact question the task brief ends
// on: can a student read another contest's business, a disqualified
// participant's own contest, or a contest that is not running, through
// Access — the one admission this package now shares between the SQL console
// and the participant-facing story/questions endpoints.
func TestAccessAgainstTheRealSchemaAnswersTheOwnersOwnStandingCheck(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	enrolled := makeIntegrationUser(t, ctx, pool, "enrolled-"+uuid.NewString()[:8])
	stranger := makeIntegrationUser(t, ctx, pool, "stranger-"+uuid.NewString()[:8])
	disqualified := makeIntegrationUser(t, ctx, pool, "disq-"+uuid.NewString()[:8])

	farFuture := time.Now().Add(24 * time.Hour)
	running := makeRunningFixedContest(t, ctx, pool, author, farFuture)
	elsewhere := makeRunningFixedContest(t, ctx, pool, author, farFuture)
	past := time.Now().Add(-time.Minute)
	notRunningAnymore := makeRunningFixedContest(t, ctx, pool, author, past)
	for _, id := range []uuid.UUID{running, elsewhere, notRunningAnymore} {
		t.Cleanup(func() {
			clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
		})
	}

	registrations := postgres.NewRegistrations(pool)
	if _, err := registrations.Add(ctx, running, enrolled); err != nil {
		t.Fatalf("enrol the participant: %v", err)
	}
	if _, err := registrations.Add(ctx, elsewhere, stranger); err != nil {
		t.Fatalf("enrol the stranger elsewhere: %v", err)
	}
	if _, err := registrations.Add(ctx, running, disqualified); err != nil {
		t.Fatalf("enrol the participant to disqualify: %v", err)
	}
	disqualifiedReg, err := registrations.ByUser(ctx, running, disqualified)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if err := registrations.SetStatus(ctx, disqualifiedReg.ID, contests.RegistrationDisqualified); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}
	if _, err := registrations.Add(ctx, notRunningAnymore, enrolled); err != nil {
		t.Fatalf("enrol into the finished contest: %v", err)
	}

	service := queryproxy.New(
		registrations, postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{},
	)

	// The enrolled participant of the running contest gets in.
	if _, _, err := service.Access(ctx, running, enrolled, netip.Addr{}); err != nil {
		t.Fatalf("an enrolled participant of a running contest was refused: %v", err)
	}

	// A stranger to this contest — enrolled somewhere else entirely — gets
	// exactly the same refusal a caller naming a contest ID that names
	// nothing at all would (ErrNotAParticipant), never a 404 that would
	// confirm this contest exists and never anything else about it.
	if _, _, err := service.Access(ctx, running, stranger, netip.Addr{}); !errors.Is(err, queryproxy.ErrNotAParticipant) {
		t.Fatalf("a stranger to this contest: error = %v, want ErrNotAParticipant", err)
	}

	// A contest ID that names nothing at all reads the same way.
	if _, _, err := service.Access(ctx, uuid.New(), enrolled, netip.Addr{}); !errors.Is(err, queryproxy.ErrNotAParticipant) {
		t.Fatalf("a contest that does not exist: error = %v, want ErrNotAParticipant", err)
	}

	// A disqualified participant of this very contest is refused the same
	// way — disqualification must not read as "not registered" to the
	// caller, but it must read as the same code a stranger gets.
	if _, _, err := service.Access(ctx, running, disqualified, netip.Addr{}); !errors.Is(err, queryproxy.ErrNotAParticipant) {
		t.Fatalf("a disqualified participant: error = %v, want ErrNotAParticipant", err)
	}

	// A contest past its own ends_at is refused even though nothing here ever
	// flips contests.status to finished — the same deadline formula Run
	// checks before taking a query.
	if _, _, err := service.Access(ctx, notRunningAnymore, enrolled, netip.Addr{}); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("a contest past its own deadline: error = %v, want ErrContestNotRunning", err)
	}
}

// TestTheConsoleClosesOnceNothingIsAnswerableAgainstTheRealSchema is the
// same guarantee CLAUDE.md rule 11 asks for: the fact that closes the console
// is decided in SQL (postgres.Answerable) and applied in Go
// (queryproxy.Service.Run), so it is proven across that boundary rather than
// on either side of it. The fakes elsewhere in this package cannot see a
// disagreement between the query and the schema; this can.
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

	// Direct SQL for the same reason the contest fixtures above use it:
	// postgres.Questions.Create insists on the authoring transaction, which
	// is the contest module's own concern and only noise here.
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

	service := queryproxy.New(
		registrations,
		postgres.NewContests(pool),
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"},
		&runner{result: &queryrunner.Result{Columns: []string{"a"}}},
	).WithAnswerable(postgres.NewAnswerable(pool))

	cmd := queryproxy.Command{ContestID: contestID, UserID: student, SQL: `SELECT 1`, RequestID: uuid.New()}

	// One question, one attempt, nothing spent: the console is open.
	if _, err := service.Run(ctx, cmd); err != nil {
		t.Fatalf("a query while the contest's only question is still open: %v", err)
	}

	// Spending that one attempt closes the question, and with it the console.
	if _, err := postgres.NewSubmissions(pool).Insert(ctx, contests.SubmissionRequest{
		RegistrationID: registration.ID, QuestionID: questionID, Value: "wrong", IsCorrect: false,
		Points: 5, MaxAttempts: &max, Deadline: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}

	if _, err := service.Run(ctx, cmd); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("a query with every question closed: error = %v, want ErrNothingLeftToAnswer", err)
	}

	// And nothing else closed with it: the play screen's own admission still
	// admits them, which is the whole reason this is not ErrFinished.
	if _, _, err := service.Access(ctx, contestID, student, netip.Addr{}); err != nil {
		t.Fatalf("Access() = %v, want nil — only the console closes", err)
	}
}

// Under individual timing the first read of the contest's content starts the
// participant's clock, against the real registrations table: reading the
// questions starts it, the events channel's admission never does, and a
// second read later does not move started_at (CLAUDE.md rule 10 — the
// once-only guarantee lives in the repository's own WHERE clause, which the
// fakes cannot show).
func TestAnIndividualParticipantsFirstReadStartsTheClockOnceAndTheEventsChannelNever(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeIntegrationUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeIntegrationUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	clock := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	contestID := makeRunningIndividualContest(t, ctx, pool, author, 60, clock.Add(-time.Hour), clock.Add(24*time.Hour))
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})

	registrations := postgres.NewRegistrations(pool)
	if _, err := registrations.Add(ctx, contestID, student); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	service := queryproxy.New(registrations, postgres.NewContests(pool), nil, nil, nil).
		WithClock(func() time.Time { return clock })

	if _, _, err := service.AccessForEvents(ctx, contestID, student, netip.Addr{}); err != nil {
		t.Fatalf("AccessForEvents() = %v", err)
	}
	if stored, err := registrations.ByUser(ctx, contestID, student); err != nil || stored.StartedAt != nil {
		t.Fatalf("after the events channel: StartedAt = %v, err = %v; want no clock started", stored.StartedAt, err)
	}

	participant, contest, err := service.Access(ctx, contestID, student, netip.Addr{})
	if err != nil {
		t.Fatalf("Access() = %v", err)
	}
	if _, err := service.StartOnRead(ctx, contest, participant); err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	stored, err := registrations.ByUser(ctx, contestID, student)
	if err != nil || stored.StartedAt == nil || !stored.StartedAt.Equal(clock) {
		t.Fatalf("after the first read: StartedAt = %v, err = %v; want %v", stored.StartedAt, err, clock)
	}

	// A later read by a request still holding the participant as it was
	// before the start, the way a racing read would: the stored start stays
	// where it is.
	clock = clock.Add(7 * time.Minute)
	if _, err := service.StartOnRead(ctx, contest, participant); err != nil {
		t.Fatalf("second StartOnRead() = %v", err)
	}
	again, err := registrations.ByUser(ctx, contestID, student)
	if err != nil || again.StartedAt == nil || !again.StartedAt.Equal(*stored.StartedAt) {
		t.Fatalf("after a second read: StartedAt = %v, err = %v; want %v unchanged", again.StartedAt, err, stored.StartedAt)
	}
}

// countingTracer counts every statement pgx sends over the wire, through the
// same pgx.QueryTracer hook storagetest.Open's own `configure` callback
// exists to install — a count of round trips a real connection actually
// made, not of Go-level calls into a fake.
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

// TestRunsCoreRoundTripsAreMeasured counts how many statements one
// steady-state Run call sends to the core database, with a real
// pgx.QueryTracer against dbcontest_core_test rather than assumed from
// reading the code. The scenario is the common case an olympiad spends
// almost all of its queries in — a fixed-timing, read-only contest, a
// participant who has already started and already has a current game
// database — so neither Start nor Quota adds a round trip of its own, and
// what is left is exactly the lookup this collapses to one query.
//
// The commit before WithLookup existed measured 5 the same way (People,
// Contests, Games and Answerable each their own call, plus Ensure); this one
// measures 3 (the combined lookup, Answerable, Ensure) — two fewer per query,
// on every query the console runs.
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
	// The names are unique per run: both columns are unique across the
	// installation, and a committed fixture must not collide with a
	// concurrent or leftover run.
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
	if _, err := registrations.Start(ctx, registration.ID, time.Now()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	games := postgres.NewGameInstances(pool)
	if err := games.Assign(ctx, contestID, registration.ID, "game_db_roundtrip_"+suffix, 1); err != nil {
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
	// One combined lookup (participant + contest + game), one AnswerableLeft,
	// one Ensure (→ repo.Of). A regression that reopens either of the two
	// round trips the lookup collapsed changes this number.
	if got != 3 {
		t.Errorf("core round trips = %d, want 3 (one combined lookup, one AnswerableLeft, one Ensure)", got)
	}
}
