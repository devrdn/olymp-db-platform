package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
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

// sourceGame creates a small game on the test cluster with the writable
// table, granted as a template build grants it.
func sourceGame(t *testing.T) string {
	t.Helper()

	source := gamedbtest.Scratch(t)
	gamedbtest.Run(t, source,
		`CREATE TABLE `+writableTable+` (id int PRIMARY KEY, case_id int NOT NULL, description text NOT NULL)`,
		`INSERT INTO `+writableTable+` VALUES (1, 1, 'a torn train ticket')`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter)
	return source
}

// readOnlyFixture sets up one participant in a read-only contest, with its
// copy when provisioned, and tears it down at the end.
func readOnlyFixture(t *testing.T, st *stores, provisioned bool) *fixture {
	t.Helper()

	source := sourceGame(t)
	f := &fixture{}
	tearDownAtTheEnd(t, st, f)
	if _, err := setup(t.Context(), st, f, source, 1, contests.ModeReadOnly); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if provisioned {
		if _, err := provision(t.Context(), st, f, 1, 1, nil); err != nil {
			t.Fatalf("provision: %v", err)
		}
	}
	return f
}

// tearDownAtTheEnd removes whatever of f a failed test left, and says what
// could not be removed. Teardown of an already removed fixture finds nothing.
func tearDownAtTheEnd(t *testing.T, st *stores, f *fixture) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := teardown(ctx, st, f); err != nil {
			t.Logf("tearing down the fixture %s: %v", f.RunID, err)
		}
	})
}

// assertNothingLeft checks that no account, contest or database of f
// survived.
func assertNothingLeft(t *testing.T, st *stores, f *fixture) {
	t.Helper()

	var accounts, contestRows, databases int
	if err := st.core.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE id = ANY($1)`,
		f.userIDs()).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := st.core.QueryRow(t.Context(), `SELECT count(*) FROM contests WHERE id = $1`,
		f.ContestID).Scan(&contestRows); err != nil {
		t.Fatal(err)
	}
	if err := st.game.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_database
		WHERE EXISTS (SELECT 1 FROM unnest($1::text[]) AS p WHERE left(datname, length(p)) = p)`,
		databasePrefixes([]uuid.UUID{f.ContestID})).Scan(&databases); err != nil {
		t.Fatal(err)
	}
	if accounts+contestRows+databases != 0 {
		t.Fatalf("left behind: %d accounts, %d contests, %d databases", accounts, contestRows, databases)
	}
}
