package main

import (
	"context"
	"os"
	"strings"
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

func TestTheReportLeadsWithTheVerdict(t *testing.T) {
	out, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	slow := runSummary{Name: "steady-40", Queries: 100, All: latency{Count: 100, P95: 4000}}
	v := profiles["beta"].thresholds.judge([]runSummary{slow})
	rep := &report{Profile: "beta", Verdict: &v, Runs: []runSummary{slow}, Mix: mix{}}

	if err := writeReport(out, rep); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	text := string(mustRead(t, out, "report.md"))
	verdictAt, runsAt := strings.Index(text, "## Verdict of the beta profile"), strings.Index(text, "## Runs")
	if verdictAt < 0 || verdictAt > runsAt || !strings.Contains(text, "**FAILED**") {
		t.Fatalf("the report does not lead with a failed verdict:\n%s", text)
	}
	if !strings.Contains(string(mustRead(t, out, "report.json")), `"passed": false`) {
		t.Error("report.json lacks the verdict")
	}
}

func mustRead(t *testing.T, out *os.Root, name string) []byte {
	t.Helper()
	body, err := out.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
