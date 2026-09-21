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
