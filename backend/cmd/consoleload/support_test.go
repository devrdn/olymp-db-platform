package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

// testStores connects to the test clusters in CORE_DB_DSN and GAME_DB_DSN,
// as openStores connects to an installation's, and skips without either.
func testStores(t *testing.T) *stores {
	t.Helper()

	if os.Getenv(storagetest.CoreDSNVar) == "" || !gamedbtest.Configured() {
		t.Skip("set CORE_DB_DSN and GAME_DB_DSN to test cluster databases to run the database tests")
	}
	core, err := storagetest.OpenCore(t.Context(), nil)
	if err != nil {
		t.Fatalf("open the core test database: %v", err)
	}
	t.Cleanup(core.Close)

	// The game pool is gamedbtest's, shared by the whole binary, so it is
	// not closed here.
	game := gamedbtest.Admin(t)
	user, password := gamedbtest.AdminCredentials(t)
	provisioner, err := gamedb.NewProvisioner(game, gamedbtest.DSN(t, user, password, "postgres"), "")
	if err != nil {
		t.Fatalf("open the provisioner: %v", err)
	}
	return &stores{core: core, game: game, provisioner: provisioner, drop: provisioner.Drop}
}

func testAccount(t *testing.T, st *stores, login, status string) uuid.UUID {
	t.Helper()

	u, err := postgres.NewUsers(st.core).Create(t.Context(), users.User{
		Login: login, FullName: login, Status: status, PasswordHash: mustHash(randomSecret()),
	})
	if err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() {
		_, _ = st.core.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, u.ID)
	})
	return u.ID
}

func testContest(t *testing.T, st *stores, owner uuid.UUID, status string, starts time.Time) uuid.UUID {
	t.Helper()

	ends := starts.Add(3 * time.Hour)
	c, err := postgres.NewContests(st.core).Create(t.Context(), contests.Contest{
		Status: status, Enrollment: contests.EnrollmentInviteOnly, QuestionMode: contests.QuestionModeMulti,
		Progression: contests.ProgressionFree, Scoring: contests.ScoringPoints, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends, CreatedBy: owner,
		LeaderboardNames: contests.LeaderboardNamesLogin, ICPCPenaltyMin: contests.DefaultICPCPenaltyMin,
	})
	if err != nil {
		t.Fatalf("create a %s contest: %v", status, err)
	}
	// Registered after the owner's, so it runs first.
	t.Cleanup(func() {
		_, _ = st.core.Exec(context.WithoutCancel(t.Context()), `DELETE FROM contests WHERE id = $1`, c.ID)
	})
	return c.ID
}

func register(t *testing.T, st *stores, contest, user uuid.UUID) {
	t.Helper()

	if _, err := postgres.NewRegistrations(st.core).Add(t.Context(), contest, user); err != nil {
		t.Fatalf("register %s in %s: %v", user, contest, err)
	}
}
