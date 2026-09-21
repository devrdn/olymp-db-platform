package profile_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

// answered builds the answers tab of one question: the attempts given, in
// order, at a minute apart.
func answered(question uuid.UUID, ord int, correct ...bool) monitor.QuestionAttempts {
	group := monitor.QuestionAttempts{QuestionID: question, QuestionOrd: ord}
	for i, ok := range correct {
		points := 0
		if ok {
			points = 10
		}
		group.Attempts = append(group.Attempts, monitor.Attempt{
			AnswerData: monitor.AnswerData{QuestionID: question, QuestionOrd: ord,
				AttemptNo: i + 1, Correct: ok, PointsAwarded: points},
			At: start.Add(time.Duration(i+1) * time.Minute),
		})
	}
	return group
}

func (r *rig) report(t *testing.T, status string) profile.Report {
	t.Helper()
	c, p := r.seed(t, status)
	started := start
	p.StartedAt = &started
	r.people.Put(p)
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true,
		Scoring: contests.ScoringPoints, Place: 3, Participants: 11,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 10, Solved: 1}}}

	access, err := r.service.Open(t.Context(), c.ID, r.user)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	report, err := r.service.Report(t.Context(), access)
	if err != nil {
		t.Fatalf("Report() = %v", err)
	}
	return report
}

func TestTheReportCollectsTheQuestionsFromTheAnswersTab(t *testing.T) {
	r := newRig(t)
	solved, missed := uuid.New(), uuid.New()
	r.attempts.answers = monitor.Answers{Questions: []monitor.QuestionAttempts{
		answered(solved, 1, false, true),
		answered(missed, 2, false, false, false),
	}}

	report := r.report(t, contests.StatusFinished)
	if len(report.Questions) != 2 {
		t.Fatalf("Report() has %d questions", len(report.Questions))
	}
	first := report.Questions[0]
	if first.QuestionID != solved || first.Ord != 1 || first.Attempts != 2 || !first.Solved ||
		first.Points != 10 || first.SolvedAt == nil || !first.SolvedAt.Equal(start.Add(2*time.Minute)) {
		t.Errorf("the solved question reads %+v", first)
	}
	second := report.Questions[1]
	if second.Attempts != 3 || second.Solved || second.SolvedAt != nil || second.Points != 0 {
		t.Errorf("the unsolved question reads %+v", second)
	}
	if r.attempts.asks != 1 {
		t.Errorf("the answers tab was read %d times, want one", r.attempts.asks)
	}
}

func TestTheReportCarriesTheResultAndThePlaceOfAnOpenTable(t *testing.T) {
	r := newRig(t)
	report := r.report(t, contests.StatusFinished)
	if !report.Result.PlaceOpen || report.Result.Place != 3 || report.Result.Participants != 11 {
		t.Errorf("result = %+v, want third of eleven", report.Result)
	}
	if report.Result.Points != 10 || report.Result.Solved != 1 {
		t.Errorf("result = %+v, want the participant's own numbers", report.Result)
	}
}

// A registration the leaderboard has no row for gets its report with no
// result at all — nil, not a zeroed one.
//
// The table is bounded (leaderboard.DefaultMaxRows), and a registration below
// the cut is on no computation of it, so this is every participant of a large
// contest past the two thousandth row, not a corner case. A zeroed Result
// would carry an empty scoring and an empty state, which read as a result of
// nought in a mode nobody can name.
func TestTheReportOfARegistrationTheTableHasNoRowFor(t *testing.T) {
	r := newRig(t)
	c, p := r.seed(t, contests.StatusFinished)
	started := start
	p.StartedAt = &started
	r.people.Put(p)
	r.attempts.answers = monitor.Answers{Questions: []monitor.QuestionAttempts{answered(uuid.New(), 1, true)}}

	access, err := r.service.Open(t.Context(), c.ID, r.user)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	report, err := r.service.Report(t.Context(), access)
	if err != nil {
		t.Fatalf("Report() = %v", err)
	}
	if report.Result != nil {
		t.Errorf("result = %+v, want none at all", report.Result)
	}
	// The rest of the report is still the participant's own, and still theirs
	// to read: the standing is the only thing the table was asked for.
	if len(report.Questions) != 1 || !report.Questions[0].Solved {
		t.Errorf("questions = %+v, want the participant's own answers", report.Questions)
	}
}

func TestTheReportCountsTheQueriesAndTheTimeWorked(t *testing.T) {
	r := newRig(t)
	last := start.Add(80 * time.Minute)
	r.store.activity = profile.Activity{Queries: 31, Successful: 24, LastAnswerAt: &last}

	report := r.report(t, contests.StatusFinished)
	if report.Activity.Queries != 31 || report.Activity.Successful != 24 {
		t.Errorf("activity = %+v", report.Activity)
	}
	worked, ok := report.Worked()
	if !ok || worked != 80*time.Minute {
		t.Errorf("Worked() = %v, %v, want 80 minutes from the start of the clock to the last answer", worked, ok)
	}
}

// A participant who never started the clock, or never answered, has no
// stretch of time to report — not a zero one.
func TestTheTimeWorkedIsAbsentWithoutAStartAndAnAnswer(t *testing.T) {
	r := newRig(t)
	report := r.report(t, contests.StatusFinished)
	if worked, ok := report.Worked(); ok {
		t.Errorf("Worked() = %v, %v, want no answer at all", worked, ok)
	}
}

// In ICPC scoring a question's share of the penalty is what the report has
// to show: the server writes points_awarded = 0 for every ICPC submission,
// so a column of points would read nought on a row that cost fifty minutes.
//
// The number is not derived here. It is leaderboard.Cell.Penalty over the
// cell the table already computed for that question, with the contest's own
// icpc_penalty_min — the same arithmetic the standings statement performs
// (TestICPCCellPenaltiesAddUpToTheRowsOwn holds the two together).
func TestTheReportCarriesEachQuestionsShareOfTheICPCPenalty(t *testing.T) {
	r := newRig(t)
	solved, missed := uuid.New(), uuid.New()
	r.attempts.answers = monitor.Answers{Questions: []monitor.QuestionAttempts{
		answered(solved, 1, false, true),
		answered(missed, 2, false),
	}}

	c, p := r.seed(t, contests.StatusFinished)
	c.Scoring, c.ICPCPenaltyMin = contests.ScoringICPC, 20
	r.contests.Put(c)
	started := start
	p.StartedAt = &started
	r.people.Put(p)
	solvedAt := start.Add(2 * time.Minute)
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true,
		Scoring: contests.ScoringICPC, Place: 2, Participants: 9, Questions: 2,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Solved: 1, Penalty: 50, Cells: []leaderboard.Cell{
			{QuestionID: solved, SolvedAt: &solvedAt, Minute: 30, Wrong: 1},
			{QuestionID: missed, Wrong: 1},
		}}}}

	access, err := r.service.Open(t.Context(), c.ID, r.user)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	report, err := r.service.Report(t.Context(), access)
	if err != nil {
		t.Fatalf("Report() = %v", err)
	}
	if len(report.Questions) != 2 {
		t.Fatalf("Report() has %d questions", len(report.Questions))
	}
	// 30 minutes plus one wrong attempt at 20: the whole of the row's 50.
	if report.Questions[0].Penalty != 50 {
		t.Errorf("the solved question cost %d, want 50", report.Questions[0].Penalty)
	}
	// An unsolved question costs nothing, however many attempts it took.
	if report.Questions[1].Penalty != 0 {
		t.Errorf("the unsolved question cost %d, want nothing", report.Questions[1].Penalty)
	}
}

// Outside ICPC nothing charges minutes, so no question has a penalty — and
// the points the contest recorded are still the result.
func TestTheReportChargesNoPenaltyOutsideICPC(t *testing.T) {
	r := newRig(t)
	solved := uuid.New()
	r.attempts.answers = monitor.Answers{Questions: []monitor.QuestionAttempts{answered(solved, 1, true)}}

	report := r.report(t, contests.StatusFinished)
	if report.Questions[0].Penalty != 0 || report.Questions[0].Points != 10 {
		t.Errorf("the question reads %+v, want 10 points and no penalty", report.Questions[0])
	}
}
