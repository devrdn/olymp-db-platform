package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

// A question with nothing ordered before it is trivially open.
func TestSequenceOpenIsTrueForTheFirstQuestion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-seq-1")
		student := makeUser(t, ctx, "student-seq-1")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q1, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		open, err := NewSequence(testPool).Open(ctx, contestID, registrationID, q1.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if !open {
			t.Error("Open() = false, want true — nothing precedes the first question")
		}
	})
}

// §6.1.1: a question stays closed to a registration that has neither
// answered the previous one correctly nor spent every attempt on it.
func TestSequenceOpenIsFalseWhileThePreviousQuestionStandsOpen(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-seq-2")
		student := makeUser(t, ctx, "student-seq-2")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		if _, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true}); err != nil {
			t.Fatalf("Create() q1 = %v", err)
		}
		q2, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q2 = %v", err)
		}

		open, err := NewSequence(testPool).Open(ctx, contestID, registrationID, q2.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if open {
			t.Error("Open() = true, want false — question 1 has no submission yet")
		}
	})
}

// A correct answer closes the previous question and opens the next one.
func TestSequenceOpenIsTrueOnceThePreviousQuestionWasAnsweredCorrectly(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-seq-3")
		student := makeUser(t, ctx, "student-seq-3")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		q1, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q1 = %v", err)
		}
		q2, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q2 = %v", err)
		}

		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q1.ID, Value: "correct", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		open, err := NewSequence(testPool).Open(ctx, contestID, registrationID, q2.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if !open {
			t.Error("Open() = false, want true — question 1 was answered correctly")
		}
	})
}

// §6.1.1's second, decisive condition: exhausting every attempt closes a
// question exactly as a correct answer would, so a participant stuck on it
// is never locked out of the rest of the contest.
func TestSequenceOpenIsTrueOnceThePreviousQuestionsAttemptsAreSpent(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-seq-4")
		student := makeUser(t, ctx, "student-seq-4")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		max := 1
		q1, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, MaxAttempts: &max, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q1 = %v", err)
		}
		q2, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q2 = %v", err)
		}

		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q1.ID, Value: "wrong",
			MaxAttempts: &max, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		open, err := NewSequence(testPool).Open(ctx, contestID, registrationID, q2.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if !open {
			t.Error("Open() = false, want true — question 1's only attempt is already spent")
		}
	})
}

// Hidden questions (is_visible = false) count in the sequence exactly as
// visible ones do.
func TestSequenceOpenCountsAHiddenQuestion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-seq-5")
		student := makeUser(t, ctx, "student-seq-5")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		questions := NewQuestions(testPool)
		q1, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: false})
		if err != nil {
			t.Fatalf("Create() q1 = %v", err)
		}
		q2, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() q2 = %v", err)
		}

		open, err := NewSequence(testPool).Open(ctx, contestID, registrationID, q2.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if open {
			t.Error("Open() = true, want false — the hidden question 1 has no submission yet")
		}

		if _, err := NewSubmissions(testPool).Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q1.ID, Value: "correct", IsCorrect: true,
			Points: 5, Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		open, err = NewSequence(testPool).Open(ctx, contestID, registrationID, q2.Ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if !open {
			t.Error("Open() = false, want true — the hidden question 1 is now closed")
		}
	})
}
