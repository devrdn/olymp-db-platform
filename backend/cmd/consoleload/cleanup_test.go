package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

func TestRemoveRefusesWhatTheHarnessDidNotCreate(t *testing.T) {
	st := testStores(t)
	now := time.Now().UTC()

	run, other := randomHex(4), randomHex(4)
	owner := testAccount(t, st, loginPrefix+run+"-owner", users.StatusBlocked)
	participant := testAccount(t, st, loginPrefix+run+"-p001", users.StatusActive)
	otherRunsParticipant := testAccount(t, st, loginPrefix+other+"-p001", users.StatusActive)
	activeOwner := testAccount(t, st, loginPrefix+other+"-owner", users.StatusActive)
	loneActiveOwner := testAccount(t, st, loginPrefix+randomHex(4)+"-owner", users.StatusActive)
	teacher := testAccount(t, st, loginPrefix+"teacher-"+randomHex(2), users.StatusActive)
	student := testAccount(t, st, "student-"+randomHex(4), users.StatusActive)

	harness := testContest(t, st, owner, contests.StatusRunning, now)
	register(t, st, harness, participant)
	real := testContest(t, st, teacher, contests.StatusRunning, now)
	register(t, st, real, otherRunsParticipant)
	ownedByAnActiveAccount := testContest(t, st, activeOwner, contests.StatusRunning, now)
	withAStudent := testContest(t, st, owner, contests.StatusRunning, now)
	register(t, st, withAStudent, student)
	withAnotherRun := testContest(t, st, owner, contests.StatusRunning, now)
	register(t, st, withAnotherRun, otherRunsParticipant)

	tests := []struct {
		name     string
		contests []uuid.UUID
		users    []uuid.UUID
		known    []string
	}{
		{name: "an account that only begins with the prefix", users: []uuid.UUID{teacher}},
		{name: "an owner-shaped account that is not blocked", users: []uuid.UUID{loneActiveOwner}},
		{name: "a harness account registered in a contest outside the set", users: []uuid.UUID{otherRunsParticipant}},
		{name: "an account that created a contest outside the set", users: []uuid.UUID{owner}},
		{name: "a contest somebody else created", contests: []uuid.UUID{real}},
		{name: "a contest whose owner is an active account", contests: []uuid.UUID{ownedByAnActiveAccount}},
		{name: "a harness contest with a stranger registered", contests: []uuid.UUID{withAStudent}},
		{name: "a harness contest with another run's participant", contests: []uuid.UUID{withAnotherRun}},
		{name: "a database that is not the contest's", contests: []uuid.UUID{harness},
			users: []uuid.UUID{participant}, known: []string{"game_tpl_c" + short(real)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := remove(t.Context(), st, tt.contests, tt.users, tt.known)
			if !errors.Is(err, errNotTheHarness) {
				t.Fatalf("remove = %v, want %v", err, errNotTheHarness)
			}
			var accounts, contestRows int
			if err := st.core.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE id = ANY($1)`,
				tt.users).Scan(&accounts); err != nil {
				t.Fatal(err)
			}
			if err := st.core.QueryRow(t.Context(), `SELECT count(*) FROM contests WHERE id = ANY($1)`,
				tt.contests).Scan(&contestRows); err != nil {
				t.Fatal(err)
			}
			if accounts != len(tt.users) || contestRows != len(tt.contests) {
				t.Fatalf("refused, and still removed: %d of %d accounts and %d of %d contests are left",
					accounts, len(tt.users), contestRows, len(tt.contests))
			}
		})
	}
}

func TestRemoveDropsNoDatabaseOfAContestItCannotTieToTheFullID(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	now := time.Now().UTC()

	organiser := testAccount(t, st, "organiser-"+randomHex(4), users.StatusBlocked)
	real := testContest(t, st, organiser, contests.StatusFinished, now.Add(-time.Hour))
	template, copyName := "game_tpl_c"+short(real), "game_pool_c"+short(real)+"_0001"
	for _, name := range []string{template, copyName} {
		if _, err := st.game.Exec(ctx, `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { gamedbtest.Drop(name) })
	}
	owner := testAccount(t, st, loginPrefix+randomHex(4)+"-owner", users.StatusBlocked)
	harnessTwin := sibling(real)
	cloneContest(t, st, testContest(t, st, owner, contests.StatusRunning, now), harnessTwin)

	tests := []struct {
		name     string
		contests []uuid.UUID
		users    []uuid.UUID
		known    []string
		refused  bool
	}{
		{name: "a contest that does not exist, sharing the real one's prefix", contests: []uuid.UUID{sibling(real)}},
		{name: "the real template named under a contest that does not exist", contests: []uuid.UUID{sibling(real)},
			known: []string{template}},
		{name: "a harness contest whose prefix a real contest shares", contests: []uuid.UUID{harnessTwin},
			users: []uuid.UUID{owner}, refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := remove(ctx, st, tt.contests, tt.users, tt.known)
			if tt.refused != errors.Is(err, errNotTheHarness) || !tt.refused && err != nil {
				t.Fatalf("remove = %v, want refused %v", err, tt.refused)
			}
			present, err := clusterDatabases(ctx, st.game, databasePrefixes([]uuid.UUID{real}))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(present, []string{copyName, template}) {
				t.Fatalf("the real contest's databases are now %v, want %v", present, []string{copyName, template})
			}
		})
	}
}

func TestSweepListsByDefaultAndRemovesOnlyWithYes(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, false)

	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: f.RunID, force: true}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	listing := out.String()
	for _, want := range []string{"run " + f.RunID, "started", f.Participants[0].Login,
		loginPrefix + f.RunID + "-owner", f.ContestID.String(), f.Template, "sweep -yes"} {
		if !strings.Contains(listing, want) {
			t.Errorf("the listing lacks %q:\n%s", want, listing)
		}
	}
	var left int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = ANY($1)`, f.userIDs()).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != len(f.userIDs()) {
		t.Fatalf("a sweep without -yes removed accounts: %d of %d left", left, len(f.userIDs()))
	}

	if err := sweep(ctx, st, &out, sweepOptions{run: f.RunID, remove: true, force: true}); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	assertNothingLeft(t, st, f)
}

func TestSweepLeavesARunThatMayStillBeGoingUnlessForced(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, false)

	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: f.RunID, remove: true}); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	if !strings.Contains(out.String(), "-force") {
		t.Errorf("the listing does not say how to remove a young run:\n%s", out.String())
	}
	var contestRows int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM contests WHERE id = $1`, f.ContestID).Scan(&contestRows); err != nil {
		t.Fatal(err)
	}
	if contestRows != 1 {
		t.Fatal("sweep -yes removed a run that started moments ago")
	}

	// The same run, started longer ago than any profile's run lasts.
	backdate := time.Now().Add(-longestProfileRun() - time.Minute)
	if _, err := st.core.Exec(ctx, `UPDATE contests SET created_at = $2 WHERE id = $1`, f.ContestID, backdate); err != nil {
		t.Fatal(err)
	}
	if _, err := st.core.Exec(ctx, `UPDATE users SET created_at = $2 WHERE id = ANY($1)`, f.userIDs(), backdate); err != nil {
		t.Fatal(err)
	}
	if err := sweep(ctx, st, &out, sweepOptions{run: f.RunID, remove: true}); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	assertNothingLeft(t, st, f)
}

func TestSweepRemovesAnAccountTiedToNoContestOnlyWithOrphans(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	run := randomHex(4)
	lone := testAccount(t, st, loginPrefix+run+"-p001", users.StatusActive)

	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: run, remove: true, force: true}); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	if !strings.Contains(out.String(), "-orphans") {
		t.Errorf("the listing does not name the account tied to no contest:\n%s", out.String())
	}
	var left int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, lone).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatal("sweep -yes removed an account tied to no contest without -orphans")
	}

	if err := sweep(ctx, st, &out, sweepOptions{run: run, remove: true, force: true, orphans: true}); err != nil {
		t.Fatalf("sweep -yes -orphans: %v", err)
	}
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, lone).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("sweep -yes -orphans left the account")
	}
}

func TestTeardownTakesTheContestFromTheTenderFirstAndCatchesALateCopy(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, true)
	games := postgres.NewGameInstances(st.core)

	late := "game_pool_c" + short(f.ContestID) + "_late"
	liveAtFirstDrop := ""
	drop := st.drop
	// Before the fixture's own cleanup, which must not make a late copy.
	t.Cleanup(func() { st.drop = drop })
	st.drop = func(ctx context.Context, name string) error {
		if liveAtFirstDrop == "" {
			live, err := games.Live(ctx)
			if err != nil {
				return err
			}
			liveAtFirstDrop = "no"
			if slices.ContainsFunc(live, func(c provisioning.Contest) bool { return c.ID == f.ContestID }) {
				liveAtFirstDrop = "yes"
			}
		}
		if name == f.Template {
			// A copy the tender began before the contest left its reach,
			// finishing while the drops run.
			if _, err := st.game.Exec(ctx, `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(late)+
				` TEMPLATE `+sqlpolicy.QuoteIdentifier(f.Template)); err != nil {
				return err
			}
		}
		return drop(ctx, name)
	}

	if err := teardown(ctx, st, f); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if liveAtFirstDrop != "no" {
		t.Errorf("the tender could still stock the contest when the drops began (live: %s)", liveAtFirstDrop)
	}
	assertNothingLeft(t, st, f)
}

func TestATeardownStoppedHalfwayLeavesWhatSweepFinds(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, true)

	drop := st.drop
	t.Cleanup(func() { st.drop = drop })
	st.drop = func(ctx context.Context, name string) error {
		if name == f.Template {
			return errors.New("the tunnel dropped")
		}
		return drop(ctx, name)
	}
	if err := teardown(ctx, st, f); err == nil {
		t.Fatal("teardown reported success with the template still there")
	}

	var accounts, contestRows int
	if err := st.core.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users WHERE id = ANY($1)), (SELECT count(*) FROM contests WHERE id = $2)`,
		f.userIDs(), f.ContestID).Scan(&accounts, &contestRows); err != nil {
		t.Fatal(err)
	}
	if accounts != len(f.userIDs()) || contestRows != 1 {
		t.Fatalf("a stopped teardown left %d accounts and %d contests, so sweep cannot find the databases", accounts, contestRows)
	}
	live, err := postgres.NewGameInstances(st.core).Live(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(live, func(c provisioning.Contest) bool { return c.ID == f.ContestID }) {
		t.Error("the half-removed contest is back in the tender's reach")
	}

	st.drop = drop
	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: f.RunID, remove: true, force: true}); err != nil {
		t.Fatalf("sweep -yes: %v\n%s", err, out.String())
	}
	assertNothingLeft(t, st, f)
}
