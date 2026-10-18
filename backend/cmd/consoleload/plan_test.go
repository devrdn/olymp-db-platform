package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

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

func TestLiveContestsListsWhatIsOrSoonWillBeRunningSaveTheExemptContest(t *testing.T) {
	st := testStores(t)
	ctx := t.Context()

	organiser := testAccount(t, st, "organiser-"+randomHex(4), users.StatusBlocked)
	// A login shaped like the harness's earns no exemption.
	lookalike := testAccount(t, st, loginPrefix+randomHex(4)+"-owner", users.StatusBlocked)
	now := time.Now().UTC()
	cases := []struct {
		name     string
		owner    uuid.UUID
		status   string
		startsIn time.Duration
		live     bool
	}{
		{"running", organiser, contests.StatusRunning, -time.Hour, true},
		{"published, starting within the horizon", organiser, contests.StatusPublished, 30 * time.Minute, true},
		{"published, starting after the horizon", organiser, contests.StatusPublished, 72 * time.Hour, false},
		{"finished", organiser, contests.StatusFinished, -48 * time.Hour, false},
		{"draft", organiser, contests.StatusDraft, -time.Hour, false},
		{"running, owned by a harness-shaped login", lookalike, contests.StatusRunning, -time.Minute, true},
		{"running, the exempt one", lookalike, contests.StatusRunning, -time.Minute, false},
	}
	ids := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		ids[i] = testContest(t, st, c.owner, c.status, now.Add(c.startsIn))
	}

	live, err := liveContests(ctx, st.core, ids[len(ids)-1], time.Hour)
	if err != nil {
		t.Fatalf("liveContests: %v", err)
	}
	listed := map[uuid.UUID]liveContest{}
	for _, c := range live {
		listed[c.ID] = c
	}
	for i, c := range cases {
		if _, got := listed[ids[i]]; got != c.live {
			t.Errorf("%s: listed %v, want %v", c.name, got, c.live)
		}
	}
	if running := listed[ids[0]]; running.Status != contests.StatusRunning || running.StartsAt == nil {
		t.Errorf("the running contest is listed as %+v", running)
	}
}

func TestRefuseLiveNamesEveryContestThatBlocksTheRun(t *testing.T) {
	if err := refuseLive(nil, time.Hour); err != nil {
		t.Fatalf("refused with nothing live: %v", err)
	}
	starts := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	running, soon := uuid.New(), uuid.New()
	err := refuseLive([]liveContest{
		{ID: running, Status: contests.StatusRunning, StartsAt: &starts},
		{ID: soon, Status: contests.StatusPublished, StartsAt: &starts},
	}, time.Hour)
	if err == nil {
		t.Fatal("not refused with two contests live")
	}
	for _, want := range []string{running.String(), soon.String(), "running", "published", "2026-10-21 09:00",
		"-allow-live-contests", "sweep -yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
}

func TestTheLiveHorizonCoversThePlannedRun(t *testing.T) {
	beta, err := parseFlags([]string{"-source-template", "game_tpl_x", "-profile", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := beta.horizon(), setupMargin+30*time.Second+15*time.Minute; got != want {
		t.Errorf("the beta profile's horizon is %s, want %s", got, want)
	}

	long, err := parseFlags([]string{"-source-template", "game_tpl_x", "-participants", "30,40",
		"-shapes", "steady,burst", "-steady-duration", "3h"})
	if err != nil {
		t.Fatal(err)
	}
	// Two counts, each a steady run and a burst run of five rounds, every run
	// after a 30 s cooldown.
	burst := 5*time.Second + 4*30*time.Second
	if got, want := long.horizon(), setupMargin+4*30*time.Second+2*3*time.Hour+2*burst; got != want {
		t.Errorf("the horizon of two long runs is %s, want %s", got, want)
	}
}

func TestAFailedTeardownSaysHowToFinishIt(t *testing.T) {
	err := teardownFailed("0123abcd", errors.New("drop game_tpl_c0123: connection reset"))
	if want := "consoleload sweep -run 0123abcd -yes -force"; !strings.Contains(err.Error(), want) {
		t.Fatalf("the error lacks %q: %v", want, err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("the error lost its cause: %v", err)
	}
}
