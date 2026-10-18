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
	"github.com/jackc/pgx/v5"
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
	manager := testAccount(t, st, loginPrefix+randomHex(4)+"-p001", users.StatusActive)
	if _, err := st.core.Exec(t.Context(), `
		INSERT INTO contest_managers (contest_id, user_id, role, granted_by) VALUES ($1, $2, 'manager', $3)`,
		real, manager, teacher); err != nil {
		t.Fatal(err)
	}

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
		{name: "a harness account that manages a contest outside the set", users: []uuid.UUID{manager}},
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
		// Only the contest, so the shared prefix is the one reason to refuse.
		{name: "a harness contest whose prefix a real contest shares", contests: []uuid.UUID{harnessTwin}, refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := remove(ctx, st, tt.contests, tt.users, tt.known)
			if tt.refused != errors.Is(err, errNotTheHarness) || !tt.refused && err != nil {
				t.Fatalf("remove = %v, want refused %v", err, tt.refused)
			}
			if tt.refused && !strings.Contains(err.Error(), "database prefix") {
				t.Fatalf("refused for another reason than the shared prefix: %v", err)
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

func TestTeardownLeavesTheAuditTrailOfEverythingItDidNotCreate(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, false)

	organiser := testAccount(t, st, "organiser-"+randomHex(4), users.StatusBlocked)
	real := testContest(t, st, organiser, contests.StatusFinished, time.Now().UTC().Add(-time.Hour))
	participant := f.Participants[0].UserID
	entry := func(actor *uuid.UUID, entity string, id uuid.UUID) int64 {
		var n int64
		if err := st.core.QueryRow(ctx, `
			INSERT INTO audit_log (actor_id, action, entity, entity_id) VALUES ($1, 'contest.update', $2, $3)
			RETURNING id`, actor, entity, id.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = st.core.Exec(context.WithoutCancel(ctx), `DELETE FROM audit_log WHERE id = $1`, n)
		})
		return n
	}
	kept := []int64{
		entry(&organiser, "contest", real),
		entry(&organiser, "user", organiser),
		// The real contest's identifier under another kind of entity.
		entry(nil, "user", real),
	}
	gone := []int64{
		entry(nil, "contest", f.ContestID),
		entry(&participant, "contest", real),
		entry(nil, "user", participant),
	}

	// A fixture whose owner is a real contest's identifier.
	crafted := *f
	crafted.OwnerID = real
	if err := verifyFixture(ctx, st.core, &crafted); !errors.Is(err, errNotTheHarness) {
		t.Errorf("verifyFixture accepted an owner that is a contest: %v", err)
	}
	if err := teardown(ctx, st, &crafted); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	var survivors []int64
	rows, err := st.core.Query(ctx, `SELECT id FROM audit_log WHERE id = ANY($1) ORDER BY id`, append(kept, gone...))
	if err != nil {
		t.Fatal(err)
	}
	if survivors, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(survivors, kept) {
		t.Fatalf("audit entries left: %v, want exactly %v (the entries about what the run did not create)", survivors, kept)
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

	// The same run, past the end it planned.
	if _, err := st.core.Exec(ctx, `UPDATE contests SET ends_at = now() - interval '1 minute' WHERE id = $1`,
		f.ContestID); err != nil {
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
	backdate(t, st, lone)

	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: run, remove: true}); err != nil {
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

	if err := sweep(ctx, st, &out, sweepOptions{run: run, remove: true, orphans: true}); err != nil {
		t.Fatalf("sweep -yes -orphans: %v", err)
	}
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, lone).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("sweep -yes -orphans left the account")
	}
}

func TestSweepHoldsARunItCannotDateWhenTheProfilesCannotBeRead(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	run := randomHex(4)
	lone := testAccount(t, st, loginPrefix+run+"-p001", users.StatusActive)
	backdate(t, st, lone)
	// The profiles read this, and refuse a negative depth.
	t.Setenv("GAME_POOL_DEPTH", "-1")

	var out strings.Builder
	if err := sweep(ctx, st, &out, sweepOptions{run: run, remove: true, orphans: true}); err != nil {
		t.Fatalf("sweep -yes: %v", err)
	}
	var left int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, lone).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 || !strings.Contains(out.String(), "-force") {
		t.Fatalf("a run that could not be dated was removed (%d left):\n%s", left, out.String())
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

func TestTheSchedulersFinishingUpdateNeverWaitsOnTheDrops(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, true)

	// The contest at its planned end, as a sweep from then meets it: the
	// scheduler's finishing statement names it by predicate.
	if _, err := st.core.Exec(ctx, `UPDATE contests SET ends_at = now() - interval '1 minute' WHERE id = $1`,
		f.ContestID); err != nil {
		t.Fatal(err)
	}
	var waited error
	checked := false
	drop := st.drop
	t.Cleanup(func() { st.drop = drop })
	st.drop = func(ctx context.Context, name string) error {
		if !checked {
			checked = true
			waited = finishLikeTheScheduler(ctx, st, f.ContestID, 2*time.Second)
		}
		return drop(ctx, name)
	}

	if err := teardown(ctx, st, f); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !checked {
		t.Fatal("nothing was dropped, so the statement never ran against the drops")
	}
	if waited != nil {
		t.Fatalf("the scheduler's finishing statement, run while the databases dropped: %v", waited)
	}
	assertNothingLeft(t, st, f)
}

func TestATeardownWaitsOnlyMomentsForRowsAnotherSessionHolds(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	f := readOnlyFixture(t, st, false)

	holder, err := st.core.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM contests WHERE id = $1 FOR UPDATE`, f.ContestID); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	err = teardown(ctx, st, f)
	if err == nil || !strings.Contains(err.Error(), "another session held the rows") {
		t.Fatalf("teardown = %v, want a refusal naming the other session", err)
	}
	if waited := time.Since(started); waited > lockWait+2*time.Second {
		t.Errorf("teardown waited %s for the row, want about %s", waited, lockWait)
	}
	var contestRows int
	if err := st.core.QueryRow(ctx, `SELECT count(*) FROM contests WHERE id = $1`, f.ContestID).Scan(&contestRows); err != nil {
		t.Fatal(err)
	}
	if contestRows != 1 {
		t.Fatal("the refusal said the rows are still there, and the contest is gone")
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := teardown(ctx, st, f); err != nil {
		t.Fatalf("teardown after the other session let go: %v", err)
	}
	assertNothingLeft(t, st, f)
}

func TestABoundTransactionLetsGoOfItsRowsWhenItsClientGoesQuiet(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()
	contest := testContest(t, st, testAccount(t, st, loginPrefix+randomHex(4)+"-owner", users.StatusBlocked),
		contests.StatusRunning, time.Now().UTC())

	// A client that locks a row and then falls silent, as one behind a
	// tunnel that died does.
	quiet, err := st.core.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = quiet.Rollback(context.WithoutCancel(ctx)) }()
	wait := 500 * time.Millisecond
	if err := boundTransaction(ctx, quiet, wait); err != nil {
		t.Fatalf("boundTransaction: %v", err)
	}
	if _, err := quiet.Exec(ctx, `SELECT 1 FROM contests WHERE id = $1 FOR UPDATE`, contest); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * wait)

	// The server must have let the row go by now: another session gets it
	// within the same bound.
	other, err := st.core.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.WithoutCancel(ctx)) }()
	if err := boundTransaction(ctx, other, wait); err != nil {
		t.Fatalf("boundTransaction: %v", err)
	}
	if _, err := other.Exec(ctx, `SELECT 1 FROM contests WHERE id = $1 FOR UPDATE`, contest); err != nil {
		t.Fatalf("the quiet session still holds the row after %s: %v", 3*wait, err)
	}
	if _, err := quiet.Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("the quiet session is still alive, so its rows were never let go")
	}
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
