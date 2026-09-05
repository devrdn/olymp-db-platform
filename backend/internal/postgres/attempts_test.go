package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// makeSubmission inserts a raw submission row. There is no writer to call yet
// (Task 3's own repository, once the answer path exists) — this is the
// shape the schema commits to, and what Attempts.ForRegistration must read
// back correctly.
func makeSubmission(t *testing.T, ctx context.Context, registration, question uuid.UUID, attemptNo int, correct bool) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct)
		VALUES ($1, $2, $3, 'an answer', $4)`,
		registration, question, attemptNo, correct)
	if err != nil {
		t.Fatalf("insert submission: %v", err)
	}
}

func TestAttemptsForRegistrationCountsAttemptsAndCorrectness(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-attempts")
		student := makeUser(t, ctx, "student-attempts")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)

		questions := NewQuestions(testPool)
		missed, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		solved, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		untouched, err := questions.Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		makeSubmission(t, ctx, registrationID, missed.ID, 1, false)
		makeSubmission(t, ctx, registrationID, missed.ID, 2, false)
		makeSubmission(t, ctx, registrationID, solved.ID, 1, false)
		makeSubmission(t, ctx, registrationID, solved.ID, 2, true)

		stats, err := NewAttempts(testPool).ForRegistration(ctx, registrationID)
		if err != nil {
			t.Fatalf("ForRegistration() = %v", err)
		}

		if got := stats[missed.ID]; got.Attempts != 2 || got.Correct {
			t.Fatalf("missed question stats = %+v, want {Attempts: 2, Correct: false}", got)
		}
		if got := stats[solved.ID]; got.Attempts != 2 || !got.Correct {
			t.Fatalf("solved question stats = %+v, want {Attempts: 2, Correct: true}", got)
		}
		if _, ok := stats[untouched.ID]; ok {
			t.Fatalf("an untouched question had an entry: %+v", stats[untouched.ID])
		}
	})
}

// Another participant's attempts on the same question must never bleed into
// this one's count — the filter is by registration, not by question alone.
func TestAttemptsForRegistrationDoesNotSeeAnotherParticipantsAttempts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-attempts-2")
		alice := makeUser(t, ctx, "alice-attempts")
		bob := makeUser(t, ctx, "bob-attempts")
		contestID := makeContest(t, ctx, author.ID)
		aliceReg := makeRegistration(t, ctx, contestID, alice.ID)
		bobReg := makeRegistration(t, ctx, contestID, bob.ID)

		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		makeSubmission(t, ctx, aliceReg, q.ID, 1, true)
		makeSubmission(t, ctx, bobReg, q.ID, 1, false)
		makeSubmission(t, ctx, bobReg, q.ID, 2, false)

		stats, err := NewAttempts(testPool).ForRegistration(ctx, aliceReg)
		if err != nil {
			t.Fatalf("ForRegistration() = %v", err)
		}
		if got := stats[q.ID]; got.Attempts != 1 || !got.Correct {
			t.Fatalf("alice's stats = %+v, want {Attempts: 1, Correct: true} — bob's attempts must not appear", got)
		}
	})
}
