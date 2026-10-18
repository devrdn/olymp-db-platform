package main

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

func TestLiveContestsCountsOnlyOtherPeoplesContestsThatAreOrSoonWillBeRunning(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()

	before, err := liveContests(ctx, st.core)
	if err != nil {
		t.Fatalf("liveContests: %v", err)
	}

	organiser := testAccount(t, st, "organiser-"+randomHex(4))
	ours := testAccount(t, st, loginPrefix+randomHex(4)+"-owner")
	now := time.Now().UTC()
	for _, c := range []struct {
		owner    uuid.UUID
		status   string
		startsIn time.Duration
	}{
		{organiser, contests.StatusRunning, -time.Hour},         // counted
		{organiser, contests.StatusPublished, 30 * time.Minute}, // counted: starts during a run
		{organiser, contests.StatusPublished, 72 * time.Hour},
		{organiser, contests.StatusFinished, -48 * time.Hour},
		{organiser, contests.StatusDraft, -time.Hour},
		{ours, contests.StatusRunning, -time.Minute},
	} {
		testContest(t, st, c.owner, c.status, now.Add(c.startsIn))
	}

	after, err := liveContests(ctx, st.core)
	if err != nil {
		t.Fatalf("liveContests: %v", err)
	}
	if after-before != 2 {
		t.Fatalf("liveContests rose by %d, want 2", after-before)
	}
}

func testAccount(t *testing.T, st *stores, login string) uuid.UUID {
	t.Helper()

	u, err := postgres.NewUsers(st.core).Create(t.Context(), users.User{
		Login: login, FullName: login, Status: users.StatusBlocked, PasswordHash: mustHash(randomSecret()),
	})
	if err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() {
		_, _ = st.core.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, u.ID)
	})
	return u.ID
}

func testContest(t *testing.T, st *stores, owner uuid.UUID, status string, starts time.Time) {
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
}
