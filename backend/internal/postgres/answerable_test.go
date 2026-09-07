package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// A participant who has answered nothing yet has everything left to answer.
func TestAnswerableLeftIsTrueWhileAQuestionStandsUnanswered(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-1")
		student := makeUser(t, ctx, "student-answerable-1")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		if _, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true}); err != nil {
			t.Fatalf("Create() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() = false, want true — the one question has never been attempted")
		}
	})
}

// A correct answer closes a question outright, whatever attempts remain. With
// the contest's only question closed that way, nothing is left to work
// towards and the console has no reason to stay open.
func TestAnswerableLeftIsFalseOnceEveryQuestionIsAnsweredCorrectly(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-2")
		student := makeUser(t, ctx, "student-answerable-2")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "right", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() = true, want false — the only question was answered correctly")
		}
	})
}

// The other way a question closes: every attempt spent on it, none right.
func TestAnswerableLeftIsFalseOnceEveryAttemptIsSpent(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-3")
		student := makeUser(t, ctx, "student-answerable-3")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		max := 2
		q, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true, MaxAttempts: &max})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		submissions := NewSubmissions(testPool)
		for i := range max {
			if _, err := submissions.Insert(ctx, contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong", IsCorrect: false,
				Points: 5, MaxAttempts: &max, Deadline: farDeadline,
			}); err != nil {
				t.Fatalf("Insert() attempt %d = %v", i+1, err)
			}
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() = true, want false — both attempts on the only question are spent")
		}
	})
}

// A question with no attempt cap never closes by attempts, however many wrong
// answers it has taken. The console stays open, because another try really is
// still possible.
func TestAnswerableLeftStaysTrueForAQuestionWithNoAttemptCap(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-4")
		student := makeUser(t, ctx, "student-answerable-4")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		for i := range 3 {
			if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong", IsCorrect: false,
				Points: 5, Deadline: farDeadline,
			}); err != nil {
				t.Fatalf("Insert() attempt %d = %v", i+1, err)
			}
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() = false, want true — an uncapped question can always be tried again")
		}
	})
}

// One open question among closed ones is enough. The console closes when
// *nothing* is answerable, not when *something* is closed.
func TestAnswerableLeftIsTrueWhileOneOfSeveralQuestionsStandsOpen(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-5")
		student := makeUser(t, ctx, "student-answerable-5")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		solved, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() solved = %v", err)
		}
		if _, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true}); err != nil {
			t.Fatalf("Create() open = %v", err)
		}
		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: solved.ID, Value: "right", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() = false, want true — the second question has never been attempted")
		}
	})
}

// Another registration's submissions are not this one's. Two participants of
// the same contest close their questions independently, and one finishing
// must never close anybody else's console.
func TestAnswerableLeftIsPerRegistration(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-6")
		finished := makeUser(t, ctx, "student-answerable-6a")
		working := makeUser(t, ctx, "student-answerable-6b")
		contestID := makeContest(t, ctx, author.ID)
		finishedReg := makeRegistration(t, ctx, contestID, finished.ID)
		workingReg := makeRegistration(t, ctx, contestID, working.ID)
		q, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: finishedReg, QuestionID: q.ID, Value: "right", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		repo := NewAnswerable(testPool)
		left, err := repo.AnswerableLeft(ctx, contestID, finishedReg)
		if err != nil {
			t.Fatalf("AnswerableLeft() finished = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() for the participant who solved it = true, want false")
		}
		left, err = repo.AnswerableLeft(ctx, contestID, workingReg)
		if err != nil {
			t.Fatalf("AnswerableLeft() working = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() for the other participant = false, want true — they answered nothing")
		}
	})
}

// The attempt count is this registration's own, not the question's. A
// question whose cap is spent by one participant is untouched for everybody
// else — counting every registration's submissions together would close a
// contest's console for a whole room the moment its fastest competitor ran
// out of attempts.
func TestAnswerableLeftCountsOnlyThisRegistrationsAttempts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-10")
		spent := makeUser(t, ctx, "student-answerable-10a")
		fresh := makeUser(t, ctx, "student-answerable-10b")
		contestID := makeContest(t, ctx, author.ID)
		spentReg := makeRegistration(t, ctx, contestID, spent.ID)
		freshReg := makeRegistration(t, ctx, contestID, fresh.ID)
		max := 2
		q, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true, MaxAttempts: &max})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		submissions := NewSubmissions(testPool)
		for i := range max {
			if _, err := submissions.Insert(ctx, contests.SubmissionRequest{
				RegistrationID: spentReg, QuestionID: q.ID, Value: "wrong", IsCorrect: false,
				Points: 5, MaxAttempts: &max, Deadline: farDeadline,
			}); err != nil {
				t.Fatalf("Insert() attempt %d = %v", i+1, err)
			}
		}

		repo := NewAnswerable(testPool)
		left, err := repo.AnswerableLeft(ctx, contestID, spentReg)
		if err != nil {
			t.Fatalf("AnswerableLeft() spent = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() for the participant who spent both attempts = true, want false")
		}
		left, err = repo.AnswerableLeft(ctx, contestID, freshReg)
		if err != nil {
			t.Fatalf("AnswerableLeft() fresh = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() for the participant who attempted nothing = false, want true — the other one's attempts are not theirs")
		}
	})
}

// Another contest's open question is not this contest's. Without the
// contest_id filter a participant would keep the console of a contest they
// have finished for as long as any other contest anywhere had a question open.
func TestAnswerableLeftIsScopedToTheContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-7")
		student := makeUser(t, ctx, "student-answerable-7")
		contestID := makeContest(t, ctx, author.ID)
		otherID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		q, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		// Wide open, and in another contest entirely.
		if _, err := questions.Create(ctx, contests.Question{ContestID: otherID, Kind: contests.KindText, IsVisible: true}); err != nil {
			t.Fatalf("Create() other = %v", err)
		}
		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "right", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() = true, want false — the open question belongs to another contest")
		}
	})
}

// A hidden question is one a participant is never given the identifier of, so
// it can never be answered and can never close either (see
// contests.ProblemSequentialHidesQuestion). Counting it would keep the console
// open forever in every contest that has one, on the strength of a question
// nobody can work towards.
func TestAnswerableLeftIgnoresAHiddenQuestion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-8")
		student := makeUser(t, ctx, "student-answerable-8")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		q, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() visible = %v", err)
		}
		if _, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: false}); err != nil {
			t.Fatalf("Create() hidden = %v", err)
		}
		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "right", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() = true, want false — the only unclosed question is hidden and unanswerable")
		}
	})
}

// A contest that shows a participant no question at all never took anything
// away from them, so there is nothing for the console to close over. The
// refusal means "you have answered everything you were given", not "this
// contest gave you nothing" — and closing the console at minute zero of a
// contest whose only questions are hidden would be the second of those.
func TestAnswerableLeftIsTrueForAContestWithNoVisibleQuestionAtAll(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-11")
		student := makeUser(t, ctx, "student-answerable-11")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		if _, err := NewQuestions(testPool).Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: false}); err != nil {
			t.Fatalf("Create() = %v", err)
		}

		left, err := NewAnswerable(testPool).AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if !left {
			t.Error("AnswerableLeft() = false, want true — nothing was ever shown to this participant to be finished with")
		}
	})
}

// The agreement this whole check depends on, pinned against the real reader
// rather than against a second copy of its rules: whenever
// contests.Reader.Questions still shows this participant a question they may
// answer, AnswerableLeft must say there is something left. If the two ever
// disagree in that direction, a participant loses the console with work still
// in front of them.
//
// Both sides are the production types over the same rows, walked through the
// whole life of a two-question contest — nothing attempted, one wrong, one
// spent, one solved, everything closed.
func TestAnswerableLeftAgreesWithTheParticipantsOwnQuestionList(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-answerable-9")
		student := makeUser(t, ctx, "student-answerable-9")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)

		questions := NewQuestions(testPool)
		max := 2
		capped, err := questions.Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true, MaxAttempts: &max})
		if err != nil {
			t.Fatalf("Create() capped = %v", err)
		}
		solvable, err := questions.Create(ctx,
			contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true, MaxAttempts: &max})
		if err != nil {
			t.Fatalf("Create() solvable = %v", err)
		}
		for _, q := range []uuid.UUID{capped.ID, solvable.ID} {
			if err := questions.ReplaceTexts(ctx, q, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?"},
			}); err != nil {
				t.Fatalf("ReplaceTexts() = %v", err)
			}
		}

		reader := contests.NewReader(NewStories(testPool), questions, NewAttempts(testPool), NewSequence(testPool))
		repo := NewAnswerable(testPool)
		submissions := NewSubmissions(testPool)

		// After every step both sides are asked again, in both progression
		// modes: sequential narrows CanAnswer to one question at a time, and
		// the console must not close while that one is still waiting.
		agree := func(step string) {
			t.Helper()
			left, err := repo.AnswerableLeft(ctx, contestID, registrationID)
			if err != nil {
				t.Fatalf("%s: AnswerableLeft() = %v", step, err)
			}
			for _, sequential := range []bool{false, true} {
				listed, err := reader.Questions(ctx, contestID, registrationID, "en", sequential)
				if err != nil {
					t.Fatalf("%s: Questions(sequential=%v) = %v", step, sequential, err)
				}
				shown := false
				for _, q := range listed {
					if q.CanAnswer {
						shown = true
					}
					if q.CanAnswer && q.Closed {
						t.Fatalf("%s: question %s is both closed and answerable", step, q.ID)
					}
				}
				if shown && !left {
					t.Fatalf("%s (sequential=%v): the question list still offers an answer while AnswerableLeft() = false — the console would close with work left", step, sequential)
				}
				if !shown && left && !sequential {
					t.Fatalf("%s: AnswerableLeft() = true while no question in free progression may be answered", step)
				}
			}
		}

		agree("nothing attempted")

		if _, err := submissions.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: capped.ID, Value: "wrong", IsCorrect: false,
			Points: 5, MaxAttempts: &max, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() first wrong = %v", err)
		}
		agree("one wrong attempt spent")

		if _, err := submissions.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: capped.ID, Value: "wrong", IsCorrect: false,
			Points: 5, MaxAttempts: &max, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() second wrong = %v", err)
		}
		agree("the first question is out of attempts")

		if _, err := submissions.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: solvable.ID, Value: "right", IsCorrect: true,
			Points: 5, MaxAttempts: &max, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() correct = %v", err)
		}
		agree("everything closed")

		// And the end state really is the closed one, so that the walk above
		// cannot have agreed by never reaching it.
		left, err := repo.AnswerableLeft(ctx, contestID, registrationID)
		if err != nil {
			t.Fatalf("AnswerableLeft() = %v", err)
		}
		if left {
			t.Error("AnswerableLeft() = true after every question closed, want false")
		}
	})
}
