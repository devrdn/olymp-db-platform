package contests_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// runningFixedContest is a contest whose window is already open and stays
// open for the rest of the test's clock.
func runningFixedContest(f *conteststest.Fixture) contests.Contest {
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	return f.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends,
	})
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

// §6.1: a hidden question exists fully and is answerable — "not shown" and
// "not answerable" are different decisions, and Submit only ever makes the
// second one.
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

// A question naming another contest must be refused as not found, the same
// answer as a question that does not exist at all — its existence elsewhere
// is not this caller's business, and answering differently would let one
// contest's question set be probed through another's endpoint.
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

// Once a question is answered correctly, a further attempt is refused even
// though attempts remain — scoring it twice is exactly what this guards
// against.
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

// §8: the deadline check compares the core database's own clock, not the
// application server's — proven by giving the two a different answer and
// checking which one Submit actually obeyed.
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

	// The application clock still reads before the deadline; only the core
	// database's own clock has moved past it (plus grace).
	f.Submissions.Clock = func() time.Time { return ends.Add(time.Hour) }

	_, err := f.Service.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "too late",
	})
	if !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
}

// §8, finding 2: an individual participant's first answer starts their own
// clock through the very same seam queryproxy.Service.Run uses
// (RegistrationRepository.Start) — not a second implementation of "when did
// this participant begin".
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

// A reference answer whose regex does not compile must never fail the
// request or crash grading — it is treated as never matching. Answer.Validate
// already refuses this at authoring time; this is the defence-in-depth path
// for a row that reached storage some other way.
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

// Finding 3: losing the attempt-number race is retried transparently rather
// than surfaced to the caller.
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

// A repository that keeps conflicting fails loudly rather than retrying
// forever, and what Submit hands back is the participant-facing sentinel
// (finding 1: ErrTooManyAttemptConflicts, mapped to a 409 by
// internal/api/participant_handler.go) — the internal ErrAttemptConflict is
// still in the chain for anyone reading it with errors.Is, but it is not
// what the caller is meant to switch on.
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

// Exactly one transaction for a correct answer that earns something: the
// insert and the score update are one atomic unit, not two separate ones a
// crash could tear apart.
func TestSubmitRunsInsideOneTransactionWhenItScores(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
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

// Finding 5: a wrong answer needs no transaction at all — the deadline check
// and the attempt-number arithmetic are folded into Insert's own single
// statement (§8), which is atomic on its own, and there is no score update to
// share it with. Opening one anyway would be a write with no reason
// (CLAUDE.md rule 6, applied to a transaction rather than a single write).
func TestSubmitNeedsNoTransactionForAWrongAnswer(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
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

// A correct answer worth zero points needs no transaction either: nothing
// about "correct" itself requires atomicity, only a score update does, and
// this question's own Points is zero.
func TestSubmitNeedsNoTransactionForACorrectAnswerWorthNoPoints(t *testing.T) {
	f := conteststest.NewFixture()
	c := runningFixedContest(f)
	p := f.Registrations.Put(contests.Participant{ContestID: c.ID, Status: contests.RegistrationActive})
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

// Finding 2: a deliberately configured zero grace must be honoured exactly
// as queryproxy.Service.WithGrace(0) already honours it, not silently
// substituted back to five seconds because contests.NewService used to read
// zero as "unset" rather than as the deliberate choice it is.
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

	// Two seconds past the deadline: inside the five-second default grace,
	// but past a deliberately configured zero one.
	submissions.Clock = func() time.Time { return ends.Add(2 * time.Second) }

	svc := contests.NewService(contests.ServiceConfig{
		Questions: questions, Registrations: registrations, Submissions: submissions,
		UnitOfWork: &conteststest.UnitOfWork{},
		Grace:      0,
		Now:        func() time.Time { return conteststest.FixtureNow },
		Sleep:      func(time.Duration) {},
	})

	_, err := svc.Submit(t.Context(), contests.SubmitCommand{
		Participant: p, Contest: c, QuestionID: q.ID, Value: "anything",
	})
	if !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed — a configured zero grace must not become five seconds", err)
	}
}
