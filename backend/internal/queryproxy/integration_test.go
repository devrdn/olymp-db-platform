package queryproxy_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
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

// integrationPool opens the database named by CORE_DB_DSN, or skips.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		t.Skip("set CORE_DB_DSN to run this test against a real database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
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
