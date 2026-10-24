package contests_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func runningFixedContest(f *conteststest.Fixture) contests.Contest {
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	return f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends,
	})
}

func sequentialContest(f *conteststest.Fixture) contests.Contest {
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	return f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		QuestionMode: contests.QuestionModeMulti, Progression: contests.ProgressionSequential,
		StartsAt: &starts, EndsAt: &ends,
	})
}

// §6.1.1: the penalty is written once to points_awarded and never recomputed
// from the current setting.
func TestSubmitAppliesThePenaltyAtAnswerTimeAndKeepsItAfterASettingChange(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 5
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 10, PenaltyPct: 50, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	for i := 0; i < 1; i++ {
		if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
			Participant: p, Contest: c, QuestionID: q.ID, Value: "no",
		}); err != nil {
			t.Fatalf("wrong attempt %d: Submit() = %v", i+1, err)
		}
	}

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	// One wrong attempt already spent, 50% of 10 each: 10 - 1*5 = 5.
	if !outcome.Correct || outcome.PointsAwarded != 5 {
		t.Fatalf("outcome = %+v, want a correct answer worth 5 points", outcome)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 5 {
		t.Fatalf("TotalScore = %d, want 5", stored.TotalScore)
	}

	// Neither the row nor the total derived from it may move.
	q.PenaltyPct = 100
	f.Questions.Put(q)

	restored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if restored.TotalScore != 5 {
		t.Fatalf("TotalScore after the setting changed = %d, want 5 (unchanged)", restored.TotalScore)
	}
	if got := f.Submissions.All(p.ID, q.ID)[1].PointsAwarded; got != 5 {
		t.Fatalf("the stored submission's PointsAwarded = %d, want 5 (unchanged)", got)
	}
}

// §6.1.1.
func TestSubmitPenaltyNeverGoesBelowZero(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 3
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 10, PenaltyPct: 100, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	for i := 0; i < 2; i++ {
		if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
			Participant: p, Contest: c, QuestionID: q.ID, Value: "no",
		}); err != nil {
			t.Fatalf("wrong attempt %d: Submit() = %v", i+1, err)
		}
	}

	// Two wrong attempts at 100% of 10 each would demand -10.
	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct || outcome.PointsAwarded != 0 {
		t.Fatalf("outcome = %+v, want a correct answer worth 0 points, not negative", outcome)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 0 {
		t.Fatalf("TotalScore = %d, want 0", stored.TotalScore)
	}
}

// §6.1.1: further attempts are for reaching the answer, not for paying.
func TestSubmitLeavesRemainingAttemptsFreeOnceThePenaltyZeroesTheQuestion(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 5
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 10, PenaltyPct: 100, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	// The first wrong attempt alone demands the full 10 points back.
	for i := 0; i < 3; i++ {
		outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
			Participant: p, Contest: c, QuestionID: q.ID, Value: "no",
		})
		if err != nil {
			t.Fatalf("wrong attempt %d: Submit() = %v", i+1, err)
		}
		if outcome.Closed {
			t.Fatalf("wrong attempt %d reports Closed = true, want it still open", i+1)
		}
	}

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if err != nil {
		t.Fatalf("final Submit() = %v, want the question still answerable", err)
	}
	if !outcome.Correct || outcome.PointsAwarded != 0 {
		t.Fatalf("outcome = %+v, want a correct, zero-point answer", outcome)
	}
}

// §6.1.1: skipped rather than forbidden, since the scoring mode may change
// back.
func TestSubmitIgnoresThePenaltyInWinnerMode(t *testing.T) {
	f := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed, Scoring: contests.ScoringWinner,
		StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 3
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindFinal, Points: 10, PenaltyPct: 50, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "the gardener",
	}); err != nil {
		t.Fatalf("wrong attempt: Submit() = %v", err)
	}

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "The Butler",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct || outcome.PointsAwarded != 10 {
		t.Fatalf("outcome = %+v, want the full 10 points — winner mode ignores the penalty", outcome)
	}
}

// ICPC ranks by questions solved and penalty time, so a correct answer
// writes points_awarded = 0 (docs/ARCHITECTURE.md §6.1.1).
func TestSubmitAwardsNoPointsInICPCMode(t *testing.T) {
	f := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed, Scoring: contests.ScoringICPC,
		StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 10, PenaltyPct: 50, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct || outcome.PointsAwarded != 0 {
		t.Fatalf("outcome = %+v, want a correct answer worth 0 points in ICPC mode", outcome)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 0 {
		t.Fatalf("TotalScore = %d, want 0 in ICPC mode", stored.TotalScore)
	}
	if got := f.Submissions.All(p.ID, q.ID)[0].PointsAwarded; got != 0 {
		t.Fatalf("the stored submission's PointsAwarded = %d, want 0", got)
	}
}

// §6.1.1. A direct Submit call: the server enforces this, not the interface.
func TestSubmitRefusesAnUnopenedQuestionInASequentialContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := sequentialContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	f.Questions.Put(contests.Question{ContestID: c.ID, Ord: 1, Kind: contests.KindText, IsVisible: true})
	q2 := f.Questions.Put(contests.Question{ContestID: c.ID, Ord: 2, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q2.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrQuestionNotOpen) {
		t.Fatalf("error = %v, want ErrQuestionNotOpen — question 1 is not closed yet", err)
	}
}

// A locked question's refusal says only that it has not opened: checked
// before the value is held against its choices, so a stray value cannot learn
// the question's kind.
func TestSubmitRefusesALockedChoiceQuestionBeforeReadingItsChoices(t *testing.T) {
	f := conteststest.NewFixture()
	c := sequentialContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	f.Questions.Put(contests.Question{ContestID: c.ID, Ord: 1, Kind: contests.KindText, IsVisible: true})
	locked := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 2, Kind: contests.KindChoice, IsVisible: true, ChoiceIDs: []string{"a", "b"},
	})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: locked.ID, Value: "not-a-choice",
	})
	if !errors.Is(err, contests.ErrQuestionNotOpen) {
		t.Fatalf("error = %v, want ErrQuestionNotOpen, never a refusal that names the question's kind", err)
	}
}

// §6.1.1: closed also means every attempt spent.
func TestSubmitOpensTheNextQuestionOnceAttemptsAreExhausted(t *testing.T) {
	f := conteststest.NewFixture()
	c := sequentialContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 1
	q1 := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "correct"}},
	})
	q2 := f.Questions.Put(contests.Question{ContestID: c.ID, Ord: 2, Kind: contests.KindText, IsVisible: true})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q1.ID, Value: "wrong",
	}); err != nil {
		t.Fatalf("spending the only attempt on question 1: Submit() = %v", err)
	}

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q2.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() on question 2 = %v, want it open now that question 1's attempts are spent", err)
	}
}

// §6.1.1: "not shown" and "not answerable" are different decisions.
func TestSubmitSequenceCountsAHiddenQuestion(t *testing.T) {
	f := conteststest.NewFixture()
	c := sequentialContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q1 := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, IsVisible: false,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "correct"}},
	})
	q2 := f.Questions.Put(contests.Question{ContestID: c.ID, Ord: 2, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q2.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrQuestionNotOpen) {
		t.Fatalf("error = %v, want ErrQuestionNotOpen — the hidden question 1 is not closed yet", err)
	}

	// A correct answer to the hidden question unblocks question 2.
	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q1.ID, Value: "correct",
	}); err != nil {
		t.Fatalf("answering the hidden question 1 = %v", err)
	}

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q2.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() on question 2 = %v, want it open now that the hidden question 1 is closed", err)
	}
}

func TestSubmitScoresACorrectAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindFinal, Points: 10, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "The Butler",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct || outcome.PointsAwarded != 10 {
		t.Fatalf("outcome = %+v, want a correct answer worth 10 points", outcome)
	}
	if !outcome.Closed {
		t.Fatalf("outcome.Closed = false, want true — the question is answered")
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 10 {
		t.Fatalf("TotalScore = %d, want 10", stored.TotalScore)
	}
}

func TestSubmitDoesNotScoreAWrongAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindFinal, Points: 10, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "the gardener",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if outcome.Correct || outcome.PointsAwarded != 0 {
		t.Fatalf("outcome = %+v, want an incorrect, unscored answer", outcome)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 0 {
		t.Fatalf("TotalScore = %d, want 0 — a wrong answer must not score", stored.TotalScore)
	}
}

// §6.1.
func TestSubmitGradesAHiddenQuestion(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 5, IsVisible: false,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "candlestick"}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "candlestick",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct {
		t.Fatalf("outcome = %+v, want the hidden question to be gradable", outcome)
	}
}

// Refused as not found, or one contest's questions could be probed through
// another's endpoint.
func TestSubmitRefusesAQuestionFromAnotherContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	other := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: other.ID, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrQuestionNotFound) {
		t.Fatalf("error = %v, want ErrQuestionNotFound", err)
	}
}

func TestSubmitRefusesAnOverlongAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: strings.Repeat("a", 1001),
	})
	if !errors.Is(err, contests.ErrAnswerTooLong) {
		t.Fatalf("error = %v, want ErrAnswerTooLong", err)
	}
}

// Refused before grading and before any write: no attempt spent, no clock
// started.
func TestSubmitRefusesAValueThatIsNotOneOfTheChoices(t *testing.T) {
	f := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(2 * time.Hour)
	duration := 30
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationRegistered})
	limit := 2
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindChoice, Points: 10, IsVisible: true, MaxAttempts: &limit,
		ChoiceIDs: []string{"a", "b", "c"},
		Answers:   []contests.Answer{{MatchKind: contests.MatchExact, Value: "b"}},
	})

	for _, value := range []string{"", "d", "B", " b", "b ", "abc"} {
		_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
			Participant: p, Contest: c, QuestionID: q.ID, Value: value,
		})
		if !errors.Is(err, contests.ErrNotAChoice) {
			t.Errorf("Submit(%q) = %v, want ErrNotAChoice", value, err)
		}
	}
	if got := f.Submissions.All(p.ID, q.ID); len(got) != 0 {
		t.Fatalf("submissions = %d, want none written for a refused value", len(got))
	}
	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.StartedAt != nil {
		t.Fatalf("StartedAt = %v, want nil: a refused value must not start the clock", stored.StartedAt)
	}

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "a",
	})
	if err != nil {
		t.Fatalf("Submit(a) = %v", err)
	}
	if outcome.Correct || outcome.AttemptsRemaining == nil || *outcome.AttemptsRemaining != 1 {
		t.Fatalf("outcome = %+v, want a wrong first attempt with 1 left", outcome)
	}
	if got := f.Submissions.All(p.ID, q.ID); len(got) != 1 || got[0].AttemptNo != 1 {
		t.Fatalf("submissions = %+v, want exactly one, attempt 1", got)
	}
}

// An unanchored regex "b" would accept "abc" without any choice being made.
func TestSubmitRefusesAStringThatWouldMatchAChoiceRegex(t *testing.T) {
	for _, scoring := range []string{contests.ScoringPoints, contests.ScoringICPC} {
		t.Run(scoring, func(t *testing.T) {
			f := conteststest.NewFixture()
			starts := conteststest.FixtureNow.Add(-time.Hour)
			ends := conteststest.FixtureNow.Add(time.Hour)
			c := f.Contests.Put(contests.Contest{
				Status: contests.StatusRunning, Timing: contests.TimingFixed, Scoring: scoring,
				StartsAt: &starts, EndsAt: &ends,
			})
			p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
			limit := 1
			q := f.Questions.Put(contests.Question{
				ContestID: c.ID, Kind: contests.KindChoice, Points: 10, IsVisible: true, MaxAttempts: &limit,
				ChoiceIDs: []string{"a", "b", "c"},
				Answers:   []contests.Answer{{MatchKind: contests.MatchRegex, Value: "b"}},
			})

			outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: "abc",
			})
			if !errors.Is(err, contests.ErrNotAChoice) || outcome.Correct {
				t.Fatalf("Submit(abc) = %+v, %v; want ErrNotAChoice", outcome, err)
			}
			stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
			if err != nil {
				t.Fatalf("ByUser() = %v", err)
			}
			if len(f.Submissions.All(p.ID, q.ID)) != 0 || stored.TotalScore != 0 {
				t.Fatalf("submissions = %d, total score = %d; want nothing recorded",
					len(f.Submissions.All(p.ID, q.ID)), stored.TotalScore)
			}
		})
	}
}

func TestSubmitTakesFreeTextForTextAndFinalQuestions(t *testing.T) {
	for _, kind := range []string{contests.KindText, contests.KindFinal} {
		t.Run(kind, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := runningFixedContest(f)
			p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
			q := f.Questions.Put(contests.Question{
				ContestID: c.ID, Kind: kind, Points: 5, IsVisible: true,
				Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
			})

			outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: "The Butler",
			})
			if err != nil || !outcome.Correct {
				t.Fatalf("Submit() = %+v, %v; want a correct answer", outcome, err)
			}
		})
	}
}

// Refused even though attempts remain, so it cannot be scored twice.
func TestSubmitRefusesAQuestionAlreadyAnsweredCorrectly(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 10, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	}); err != nil {
		t.Fatalf("first Submit() = %v", err)
	}

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if !errors.Is(err, contests.ErrQuestionClosed) {
		t.Fatalf("second Submit() error = %v, want ErrQuestionClosed", err)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.TotalScore != 10 {
		t.Fatalf("TotalScore = %d, want 10 — the second, refused attempt must not score again", stored.TotalScore)
	}
}

func TestSubmitRefusesOnceMaxAttemptsIsSpent(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	max := 2
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "correct"}},
	})

	for i := 0; i < max; i++ {
		if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
			Participant: p, Contest: c, QuestionID: q.ID, Value: "wrong",
		}); err != nil {
			t.Fatalf("attempt %d: Submit() = %v", i+1, err)
		}
	}

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "correct",
	})
	if !errors.Is(err, contests.ErrQuestionClosed) {
		t.Fatalf("error = %v, want ErrQuestionClosed — every attempt was already spent", err)
	}
}

// §8: the database's clock decides, not the application server's.
func TestSubmitRefusesAfterTheDeadlineByTheDatabasesOwnClock(t *testing.T) {
	f := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Minute)
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	// Only the database clock has passed the deadline plus grace.
	f.Submissions.Clock = func() time.Time { return ends.Add(time.Hour) }

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "too late",
	})
	if !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
}

// §8: through RegistrationRepository.Start, the same seam
// queryproxy.Service.Run uses.
func TestSubmitStartsAnIndividualParticipantsClockOnFirstAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(2 * time.Hour)
	duration := 30
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationRegistered})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	if p.StartedAt != nil {
		t.Fatal("test setup: participant already has a start time")
	}

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.StartedAt == nil || !stored.StartedAt.Equal(conteststest.FixtureNow) {
		t.Fatalf("StartedAt = %v, want %v", stored.StartedAt, conteststest.FixtureNow)
	}
	if stored.Status != contests.RegistrationActive {
		t.Fatalf("Status = %q, want %q", stored.Status, contests.RegistrationActive)
	}
}

// Under a shared window the first answer records the start too, and the
// deadline the write is handed stays the contest's end.
func TestSubmitRecordsAFixedParticipantsStartOnFirstAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationRegistered})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if stored.StartedAt == nil || !stored.StartedAt.Equal(conteststest.FixtureNow) {
		t.Fatalf("StartedAt = %v, want %v", stored.StartedAt, conteststest.FixtureNow)
	}
	if stored.Status != contests.RegistrationActive {
		t.Fatalf("Status = %q, want %q", stored.Status, contests.RegistrationActive)
	}
	if len(f.Submissions.Requests) != 1 || !f.Submissions.Requests[0].Deadline.Equal(*c.EndsAt) {
		t.Fatalf("Insert was handed %+v, want one request with the deadline %v", f.Submissions.Requests, *c.EndsAt)
	}
}

// A second answer from a participant already started calls no Start, under
// either timing (CLAUDE.md rule 6).
func TestSubmitCallsNoStartForAParticipantAlreadyStarted(t *testing.T) {
	duration := 30
	for name, timing := range map[string]struct {
		timing      string
		durationMin *int
	}{
		"fixed":      {contests.TimingFixed, nil},
		"individual": {contests.TimingIndividual, &duration},
	} {
		t.Run(name, func(t *testing.T) {
			f := conteststest.NewFixture()
			starts, ends := f.Now.Add(-time.Hour), f.Now.Add(time.Hour)
			began := f.Now.Add(-10 * time.Minute)
			c := f.Contests.Put(contests.Contest{
				Status: contests.StatusRunning, Timing: timing.timing, DurationMin: timing.durationMin,
				StartsAt: &starts, EndsAt: &ends,
			})
			p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &began})
			q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

			if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
			}); err != nil {
				t.Fatalf("Submit() = %v", err)
			}
			if f.Registrations.StartCalls != 0 {
				t.Fatalf("Start was called %d times, want 0", f.Registrations.StartCalls)
			}
		})
	}
}

// The start's entry names the participant as its actor and lands in the
// transaction that records the start (§9.2).
func TestSubmitAuditsTheStartItRecords(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, UserID: uuid.New(), Status: contests.RegistrationRegistered})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if len(f.Audit.Entries) != 1 {
		t.Fatalf("audit entries = %v, want one %s", f.Audit.Actions(), audit.ActionParticipantStart)
	}
	got := f.Audit.Entries[0]
	if got.Action != audit.ActionParticipantStart || got.Entity != "contest" || got.EntityID != c.ID.String() {
		t.Errorf("entry = %s on %s %s, want %s on contest %s", got.Action, got.Entity, got.EntityID, audit.ActionParticipantStart, c.ID)
	}
	if got.ActorID == nil || *got.ActorID != p.UserID {
		t.Errorf("actor = %v, want the participant %s", got.ActorID, p.UserID)
	}
	if len(f.Audit.Loose) != 0 {
		t.Errorf("entries recorded outside the transaction: %v", f.Audit.Loose)
	}
}

// A request that read the registration before another one started it calls
// Start, which records nothing, so it writes no second entry (CLAUDE.md
// rule 6).
func TestSubmitAuditsNoStartAnotherRequestRecorded(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	began := f.Now.Add(-time.Minute)
	stored := f.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: uuid.New(), Status: contests.RegistrationActive, StartedAt: &began,
	})
	unstarted := stored
	unstarted.Status, unstarted.StartedAt = contests.RegistrationRegistered, nil
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: unstarted, Contest: c, QuestionID: q.ID, Value: "anything",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if f.Registrations.StartCalls != 1 {
		t.Fatalf("Start was called %d times, want 1", f.Registrations.StartCalls)
	}
	if f.Audit.Recorded(audit.ActionParticipantStart) {
		t.Errorf("audit entries = %v, want no %s from a request that did not record the start",
			f.Audit.Actions(), audit.ActionParticipantStart)
	}
}

// A shared window that is not running yet starts nobody: the answer is
// refused before any write.
func TestSubmitInAFixedContestNotYetRunningStartsNobody(t *testing.T) {
	f := conteststest.NewFixture()
	starts, ends := f.Now.Add(time.Hour), f.Now.Add(2*time.Hour)
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusPublished, Timing: contests.TimingFixed, StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationRegistered})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	}); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("Submit() = %v, want ErrContestNotRunning", err)
	}
	if f.Registrations.StartCalls != 0 {
		t.Fatalf("Start was called %d times, want 0", f.Registrations.StartCalls)
	}
}

// As a substring match, one value listing every candidate would be graded
// correct. Each case gets a fresh fixture so attempts do not carry over.
func TestSubmitMatchesARegexAgainstTheWholeAnswer(t *testing.T) {
	for name, given := range map[string]struct {
		pattern string
		value   string
		correct bool
	}{
		"a list containing the name":                       {`(?i)john\s+smith`, "alice brown; john smith; carol white", false},
		"the name alone":                                   {`(?i)john\s+smith`, "John  Smith", true},
		"a number inside a longer number":                  {`42`, "1042", false},
		"the number alone":                                 {`42`, "42", true},
		"alternation is anchored as a group":               {`butler|gardener`, "butler did it", false},
		"the other side of the alternation":                {`butler|gardener`, "the gardener", false},
		"either alternative alone":                         {`butler|gardener`, "gardener", true},
		"a multi-line flag stays in its group":             {`(?m)butler`, "maid\nbutler", false},
		"surrounding whitespace is not part of the answer": {`butler`, "  butler\n", true},
		"an organiser's own anchors still work":            {`^(the )?butler$`, "the butler", true},
	} {
		t.Run(name, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := runningFixedContest(f)
			p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
			q := f.Questions.Put(contests.Question{
				ContestID: c.ID, Kind: contests.KindFinal, Points: 5, IsVisible: true,
				Answers: []contests.Answer{{MatchKind: contests.MatchRegex, Value: given.pattern}},
			})

			outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: given.value,
			})
			if err != nil {
				t.Fatalf("Submit() = %v", err)
			}
			if outcome.Correct != given.correct {
				t.Fatalf("pattern %q against %q: Correct = %v, want %v", given.pattern, given.value, outcome.Correct, given.correct)
			}
		})
	}
}

// Answer.Validate refuses this at authoring time; this covers a row that
// reached storage some other way.
func TestSubmitTreatsAMalformedRegexAsNeverMatching(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchRegex, Value: "("}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if err != nil {
		t.Fatalf("Submit() = %v, want no error even with a broken reference pattern", err)
	}
	if outcome.Correct {
		t.Fatalf("outcome.Correct = true, want false — a pattern that cannot compile must never match")
	}
}

func TestSubmitRetriesAfterLosingTheAttemptRace(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, Points: 1, IsVisible: true})

	f.Submissions.ConflictsRemaining = 2

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if err != nil {
		t.Fatalf("Submit() = %v, want the retry to absorb the conflict", err)
	}
	if outcome.Correct {
		t.Fatalf("outcome.Correct = true, want false (unanswerable question with no reference answer)")
	}
}

// Callers switch on ErrTooManyAttemptConflicts; ErrAttemptConflict stays in
// the chain as its cause.
func TestSubmitGivesUpAfterTooManyConflicts(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	f.Submissions.ConflictsRemaining = 1000

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if err == nil {
		t.Fatal("Submit() = nil error, want it to give up eventually")
	}
	if !errors.Is(err, contests.ErrTooManyAttemptConflicts) {
		t.Fatalf("error = %v, want it to wrap ErrTooManyAttemptConflicts", err)
	}
	if !errors.Is(err, contests.ErrAttemptConflict) {
		t.Fatalf("error = %v, want it to still wrap ErrAttemptConflict", err)
	}
}

// The insert and the score update must not be torn apart by a crash.
func TestSubmitRunsInsideOneTransactionWhenItScores(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	began := f.Now.Add(-time.Minute)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &began})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if f.UnitOfWork.Calls != 1 {
		t.Fatalf("UnitOfWork.Calls = %d, want exactly 1", f.UnitOfWork.Calls)
	}
}

// Insert is a single atomic statement (§8) and there is no score update to
// share a transaction with (CLAUDE.md rule 6).
func TestSubmitNeedsNoTransactionForAWrongAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	began := f.Now.Add(-time.Minute)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &began})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "no",
	}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if f.UnitOfWork.Calls != 0 {
		t.Fatalf("UnitOfWork.Calls = %d, want exactly 0 — a wrong answer must not open a transaction", f.UnitOfWork.Calls)
	}
}

// Only a score update requires atomicity.
func TestSubmitNeedsNoTransactionForACorrectAnswerWorthNoPoints(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	began := f.Now.Add(-time.Minute)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &began})
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Kind: contests.KindText, Points: 0, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	outcome, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "yes",
	})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if !outcome.Correct {
		t.Fatalf("outcome.Correct = false, want true")
	}
	if f.UnitOfWork.Calls != 0 {
		t.Fatalf("UnitOfWork.Calls = %d, want exactly 0 — nothing here needs to be atomic with anything else", f.UnitOfWork.Calls)
	}
}

// Zero is a real setting, not "unset"; the console honours it too.
func TestSubmitHonoursAnExplicitlyConfiguredZeroGrace(t *testing.T) {
	registrations := conteststest.NewRegistrations()
	questions := conteststest.NewQuestions()
	submissions := conteststest.NewSubmissions()

	ends := conteststest.FixtureNow
	c := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends,
	}
	p := registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	// Inside a five-second grace, past a zero one.
	submissions.Clock = func() time.Time { return ends.Add(2 * time.Second) }

	svc := contests.NewService(contests.ServiceConfig{
		Questions: questions, Registrations: registrations, Submissions: submissions,
		GameReadiness: conteststest.NewGames(),
		Covers:        conteststest.NewCovers(),
		UnitOfWork:    &conteststest.UnitOfWork{},
		Gate:          contests.NewGate(0),
		Now:           func() time.Time { return conteststest.FixtureNow },
		Sleep:         func(time.Duration) {},
	})

	_, err := svc.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed — a configured zero grace must not become five seconds", err)
	}
}

// Submit writes, so it asks the gate itself rather than rely on the answer
// route; a refusal starts no clock and writes nothing.
func TestSubmitRefusesWhatTheGateRefusesBeforeStartingOrWriting(t *testing.T) {
	duration := 30
	for name, given := range map[string]struct {
		contest     func(now time.Time) contests.Contest
		participant contests.Participant
		address     netip.Addr
		want        error
	}{
		"disqualified": {
			contest: func(now time.Time) contests.Contest {
				ends := now.Add(time.Hour)
				return contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
			},
			participant: contests.Participant{Status: contests.RegistrationDisqualified},
			want:        contests.ErrNotAParticipant,
		},
		"finished": {
			contest: func(now time.Time) contests.Contest {
				ends := now.Add(time.Hour)
				return contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
			},
			participant: contests.Participant{Status: contests.RegistrationFinished},
			want:        contests.ErrParticipantFinished,
		},
		"contest published, not started": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(time.Hour), now.Add(2*time.Hour)
				return contests.Contest{Status: contests.StatusPublished, Timing: contests.TimingFixed, StartsAt: &starts, EndsAt: &ends}
			},
			participant: contests.Participant{Status: contests.RegistrationRegistered},
			want:        contests.ErrContestNotRunning,
		},
		// An individual participant on the wrong network starts no clock.
		"address the contest does not allow": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(-time.Hour), now.Add(time.Hour)
				return contests.Contest{
					Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
					StartsAt: &starts, EndsAt: &ends, AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
				}
			},
			participant: contests.Participant{Status: contests.RegistrationRegistered},
			address:     netip.MustParseAddr("192.0.2.1"),
			want:        contests.ErrAddressNotAllowed,
		},
		// Starting has no grace: at exactly ends_at it is too late to begin.
		"unstarted at exactly ends_at": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(-time.Hour), now
				return contests.Contest{
					Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
					StartsAt: &starts, EndsAt: &ends,
				}
			},
			participant: contests.Participant{Status: contests.RegistrationRegistered},
			want:        contests.ErrDeadlinePassed,
		},
		// A fixed contest with no end has no deadline; refused as not running.
		"broken timing data": {
			contest: func(time.Time) contests.Contest {
				return contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed}
			},
			participant: contests.Participant{Status: contests.RegistrationActive},
			want:        contests.ErrContestNotRunning,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := conteststest.NewFixture().WithGrace(5 * time.Second)
			c := f.Contests.Put(given.contest(f.Now))
			given.participant.ContestID = c.ID
			p := f.Registrations.Put(given.participant)
			q := f.Questions.Put(contests.Question{
				ContestID: c.ID, Kind: contests.KindText, Points: 10, IsVisible: true,
				Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
			})

			_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: "yes", Address: given.address,
			})
			if !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
			stored, err := f.Registrations.ByUser(t.Context(), c.ID, p.UserID)
			if err != nil {
				t.Fatalf("ByUser() = %v", err)
			}
			if stored.StartedAt != p.StartedAt || stored.Status != p.Status {
				t.Fatalf("registration = %+v, want it untouched — a refused answer starts no clock", stored)
			}
			if len(f.Submissions.Requests) != 0 {
				t.Fatalf("Insert was handed %d requests, want none", len(f.Submissions.Requests))
			}
		})
	}
}

// A refused participant learns nothing about which questions exist.
func TestSubmitAsksTheGateBeforeLookingTheQuestionUp(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationDisqualified})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: uuid.New(), Value: "anything",
	})
	if !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("error = %v, want ErrNotAParticipant, not a verdict on the question", err)
	}
}

// Insert must check the same instant the gate refuses at, both for a
// participant already at work and for one this answer started.
func TestSubmitHandsInsertTheDeadlinePlusGrace(t *testing.T) {
	const grace = 5 * time.Second
	duration := 30
	for name, given := range map[string]struct {
		contest      func(now time.Time) contests.Contest
		participant  func(now time.Time) contests.Participant
		wantDeadline func(now time.Time) time.Time
	}{
		"fixed, already working": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(-time.Hour), now.Add(time.Hour)
				return contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, StartsAt: &starts, EndsAt: &ends}
			},
			participant: func(time.Time) contests.Participant {
				return contests.Participant{Status: contests.RegistrationActive}
			},
			wantDeadline: func(now time.Time) time.Time { return now.Add(time.Hour + grace) },
		},
		"individual, started ten minutes ago": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(-time.Hour), now.Add(time.Hour)
				return contests.Contest{
					Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
					StartsAt: &starts, EndsAt: &ends,
				}
			},
			participant: func(now time.Time) contests.Participant {
				started := now.Add(-10 * time.Minute)
				return contests.Participant{Status: contests.RegistrationActive, StartedAt: &started}
			},
			wantDeadline: func(now time.Time) time.Time { return now.Add(20*time.Minute + grace) },
		},
		"individual, started by this answer": {
			contest: func(now time.Time) contests.Contest {
				starts, ends := now.Add(-time.Hour), now.Add(time.Hour)
				return contests.Contest{
					Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
					StartsAt: &starts, EndsAt: &ends,
				}
			},
			participant: func(time.Time) contests.Participant {
				return contests.Participant{Status: contests.RegistrationRegistered}
			},
			wantDeadline: func(now time.Time) time.Time { return now.Add(30*time.Minute + grace) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := conteststest.NewFixture().WithGrace(grace)
			c := f.Contests.Put(given.contest(f.Now))
			participant := given.participant(f.Now)
			participant.ContestID = c.ID
			p := f.Registrations.Put(participant)
			q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

			if _, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
				Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
			}); err != nil {
				t.Fatalf("Submit() = %v", err)
			}
			if len(f.Submissions.Requests) != 1 {
				t.Fatalf("Insert was handed %d requests, want 1", len(f.Submissions.Requests))
			}
			if got, want := f.Submissions.Requests[0].Deadline, given.wantDeadline(f.Now); !got.Equal(want) {
				t.Fatalf("Insert's deadline = %v, want %v", got, want)
			}
		})
	}
}

// A request that lost the start race is handed the earlier start, whose time
// can already be up; the gate is asked again before the write.
func TestSubmitAsksTheGateAgainAfterStartingTheClock(t *testing.T) {
	f := conteststest.NewFixture()
	starts, ends := f.Now.Add(-3*time.Hour), f.Now.Add(3*time.Hour)
	longAgo := f.Now.Add(-2 * time.Hour)
	duration := 30
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
		StartsAt: &starts, EndsAt: &ends,
	})
	stored := f.Registrations.Put(contests.Participant{
		ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &longAgo,
	})
	// The registration as this request read it, before the other one started.
	unstarted := stored
	unstarted.Status, unstarted.StartedAt = contests.RegistrationRegistered, nil
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: unstarted, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
	if len(f.Submissions.Requests) != 0 {
		t.Fatalf("Insert was handed %d requests, want none", len(f.Submissions.Requests))
	}
}

// Start does not move a disqualified registration; the participant gets the
// gate's answer, not a broken start.
func TestSubmitRefusesARegistrationDisqualifiedBeforeItsClockStarted(t *testing.T) {
	f := conteststest.NewFixture()
	starts, ends := f.Now.Add(-time.Hour), f.Now.Add(time.Hour)
	duration := 30
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
		StartsAt: &starts, EndsAt: &ends,
	})
	stored := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationDisqualified})
	admitted := stored
	admitted.Status = contests.RegistrationRegistered
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: admitted, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("error = %v, want ErrNotAParticipant", err)
	}
	if len(f.Submissions.Requests) != 0 {
		t.Fatalf("Insert was handed %d requests, want none", len(f.Submissions.Requests))
	}
}

// A Start that succeeds but leaves the clock pending broke its contract;
// trusting it would mean no deadline ever runs. (An active registration with
// no start time makes the fake's Start change nothing, as the real one does.)
func TestSubmitFailsClosedWhenStartLeavesTheClockPending(t *testing.T) {
	f := conteststest.NewFixture()
	starts, ends := f.Now.Add(-time.Hour), f.Now.Add(time.Hour)
	duration := 30
	c := f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual, DurationMin: &duration,
		StartsAt: &starts, EndsAt: &ends,
	})
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
	q := f.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning: the clock never started", err)
	}
	if len(f.Submissions.Requests) != 0 {
		t.Fatalf("Insert was handed %d requests, want none", len(f.Submissions.Requests))
	}
}

// bonusRig is a running points contest with one participant, for the bonus
// rule (§6.1.1). Every question it adds is answered by "yes".
type bonusRig struct {
	t *testing.T
	f *conteststest.Fixture
	c contests.Contest
	p contests.Participant
}

func newBonusRig(t *testing.T) bonusRig {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	c.Scoring = contests.ScoringPoints
	f.Contests.Put(c)
	began := f.Now.Add(-time.Minute)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive, StartedAt: &began})
	return bonusRig{t: t, f: f, c: c, p: p}
}

func (r bonusRig) question(points, penaltyPct int, bonus bool) contests.Question {
	max := 5
	return r.f.Questions.Put(contests.Question{
		ContestID: r.c.ID, Kind: contests.KindText, Points: points, PenaltyPct: penaltyPct,
		IsBonus: bonus, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})
}

func (r bonusRig) submit(q contests.Question, value string) contests.SubmitOutcome {
	r.t.Helper()
	outcome, err := r.f.Service.Submit(r.t.Context(), contests.SubmitCommand{
		Participant: r.p, Contest: r.c, QuestionID: q.ID, Value: value,
	})
	if err != nil {
		r.t.Fatalf("Submit(%q) = %v", value, err)
	}
	return outcome
}

func (r bonusRig) total() int {
	r.t.Helper()
	stored, err := r.f.Registrations.ByUser(r.t.Context(), r.c.ID, r.p.UserID)
	if err != nil {
		r.t.Fatalf("ByUser() = %v", err)
	}
	return stored.TotalScore
}

// penalised answers a regular question once wrong, then right: 10 points at
// 50% leave 5 awarded and 5 penalty points.
func (r bonusRig) penalised() {
	r.t.Helper()
	q := r.question(10, 50, false)
	r.submit(q, "no")
	if got := r.submit(q, "yes"); got.PointsAwarded != 5 {
		r.t.Fatalf("the regular question awarded %d, want 5", got.PointsAwarded)
	}
}

func TestSubmitABonusRemovesNoMoreThanThePenaltyThereIs(t *testing.T) {
	r := newBonusRig(t)
	r.penalised()
	if got := r.total(); got != 5 {
		t.Fatalf("TotalScore before the bonus = %d, want 5", got)
	}

	outcome := r.submit(r.question(3, 0, true), "yes")
	if !outcome.Correct || outcome.PointsAwarded != 0 {
		t.Errorf("bonus outcome = %+v, want correct and no points of its own", outcome)
	}
	if got := r.total(); got != 8 {
		t.Errorf("TotalScore after a 3-point bonus = %d, want 8 (5 + 3 of the 5 penalty points)", got)
	}

	r.submit(r.question(4, 0, true), "yes")
	if got := r.total(); got != 10 {
		t.Errorf("TotalScore after a second, 4-point bonus = %d, want 10 (5 + all 5 penalty points, not 7)", got)
	}
}

func TestSubmitABonusCoversAPenaltyThatArrivesAfterIt(t *testing.T) {
	r := newBonusRig(t)
	r.submit(r.question(4, 0, true), "yes")
	if got := r.total(); got != 0 {
		t.Fatalf("TotalScore after a bonus with no penalty yet = %d, want 0", got)
	}

	r.penalised()
	if got := r.total(); got != 9 {
		t.Errorf("TotalScore = %d, want 9 (5 awarded + 4 of the 5 penalty points)", got)
	}
}

// The penalty took all ten points, so the regular answer awards nothing, and
// still the bonus has something to give back.
func TestSubmitABonusGivesBackAPenaltyThatConsumedTheWholeQuestion(t *testing.T) {
	r := newBonusRig(t)
	r.submit(r.question(6, 0, true), "yes")
	q := r.question(10, 100, false)
	r.submit(q, "no")
	if got := r.submit(q, "yes"); got.PointsAwarded != 0 {
		t.Fatalf("the regular question awarded %d, want 0", got.PointsAwarded)
	}

	if got := r.total(); got != 6 {
		t.Errorf("TotalScore = %d, want 6 (0 awarded + 6 of the 10 penalty points)", got)
	}
}

func TestSubmitABonusWithNoPenaltyToRemoveAddsNothing(t *testing.T) {
	r := newBonusRig(t)
	r.submit(r.question(10, 50, false), "yes")
	r.submit(r.question(5, 0, true), "yes")

	if got := r.total(); got != 10 {
		t.Errorf("TotalScore = %d, want 10: a bonus is worth nothing without penalty points", got)
	}
}

// Whatever penalty_pct says, and still within max_attempts.
func TestSubmitAWrongBonusAnswerCostsNothing(t *testing.T) {
	r := newBonusRig(t)
	r.penalised()
	max := 2
	bonus := r.f.Questions.Put(contests.Question{
		ContestID: r.c.ID, Kind: contests.KindText, Points: 4, PenaltyPct: 50, IsBonus: true,
		MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})

	if got := r.submit(bonus, "no"); got.Correct || got.PointsAwarded != 0 {
		t.Fatalf("outcome = %+v, want a wrong answer worth nothing", got)
	}
	if last := r.f.Submissions.Requests[len(r.f.Submissions.Requests)-1]; last.PenaltyPerAttempt != 0 || last.Points != 0 {
		t.Errorf("Insert was handed (points %d, penalty %d), want (0, 0) for a bonus", last.Points, last.PenaltyPerAttempt)
	}
	if got := r.total(); got != 5 {
		t.Errorf("TotalScore = %d, want 5, untouched by a wrong bonus answer", got)
	}

	r.submit(bonus, "yes")
	if got := r.total(); got != 9 {
		t.Errorf("TotalScore after the bonus on its second attempt = %d, want 9 (the full 4, nothing deducted)", got)
	}

	spent := r.f.Questions.Put(contests.Question{
		ContestID: r.c.ID, Kind: contests.KindText, Points: 4, IsBonus: true, MaxAttempts: &max, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "yes"}},
	})
	r.submit(spent, "no")
	r.submit(spent, "no")
	if _, err := r.f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: r.p, Contest: r.c, QuestionID: spent.ID, Value: "yes",
	}); !errors.Is(err, contests.ErrQuestionClosed) {
		t.Errorf("a third attempt on a two-attempt bonus = %v, want ErrQuestionClosed", err)
	}
}

// Its score can only change together with the row that changes it.
func TestSubmitRunsACorrectBonusInsideOneTransaction(t *testing.T) {
	r := newBonusRig(t)
	r.submit(r.question(3, 0, true), "yes")
	if r.f.UnitOfWork.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1", r.f.UnitOfWork.Calls)
	}
}

// Refused when authored (bonus.go), but a bonus that reached an ICPC contest
// anyway still moves nothing: ICPC has no points to give back.
func TestSubmitABonusOutsidePointsScoringScoresNothing(t *testing.T) {
	r := newBonusRig(t)
	r.c.Scoring = contests.ScoringICPC
	r.f.Contests.Put(r.c)

	r.submit(r.question(10, 0, false), "yes")
	r.submit(r.question(4, 0, true), "yes")

	if r.f.UnitOfWork.Calls != 0 || r.total() != 0 {
		t.Errorf("UnitOfWork.Calls = %d, TotalScore = %d; want 0, 0", r.f.UnitOfWork.Calls, r.total())
	}
}
