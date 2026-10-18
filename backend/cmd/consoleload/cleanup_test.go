package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
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

func TestSweepListsByDefaultAndRemovesOnlyWithYes(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, false)

	teacher := testAccount(t, st, loginPrefix+"teacher-"+randomHex(2), users.StatusActive)
	real := testContest(t, st, teacher, contests.StatusRunning, time.Now().UTC())
	teacherLogin := loginPrefix + "teacher-"

	var out strings.Builder
	if err := sweep(ctx, st, &out, false); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	listing := out.String()
	for _, want := range []string{f.Participants[0].Login, loginPrefix + f.RunID + "-owner", f.ContestID.String(), "-yes"} {
		if !strings.Contains(listing, want) {
			t.Errorf("the listing lacks %q:\n%s", want, listing)
		}
	}
	if strings.Contains(listing, teacherLogin) || strings.Contains(listing, real.String()) {
		t.Errorf("the listing names an account or contest the harness did not create:\n%s", listing)
	}
	var left int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = ANY($1)`, f.userIDs()).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != len(f.userIDs()) {
		t.Fatalf("a sweep without -yes removed accounts: %d of %d left", left, len(f.userIDs()))
	}

	if err := sweep(ctx, st, &out, true); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	assertNothingLeft(t, st, f)
	var survivors int
	if err := st.core.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users WHERE id = $1) + (SELECT count(*) FROM contests WHERE id = $2)`,
		teacher, real).Scan(&survivors); err != nil {
		t.Fatal(err)
	}
	if survivors != 2 {
		t.Fatalf("sweep -yes removed what the harness did not create (%d of 2 left)", survivors)
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
	if err := sweep(ctx, st, &out, true); err != nil {
		t.Fatalf("sweep -yes: %v\n%s", err, out.String())
	}
	assertNothingLeft(t, st, f)
}
