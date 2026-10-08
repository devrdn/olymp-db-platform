package conteststest

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// SequenceTarget is what one case of the contract runs against: a gate over a
// contest with no questions yet, and the repositories that write what it
// reads. The real gate derives its answer from the questions and submissions
// tables, so the contract arranges state the way production does — questions
// through QuestionRepository, answers through SubmissionRepository.Insert —
// and never through anything only a fake could offer.
type SequenceTarget struct {
	Gate        contests.SequentialGate
	Questions   contests.QuestionRepository
	Submissions contests.SubmissionRepository
	// ContestID is the contest questions are created in, and RegistrationID
	// a participant registered for it.
	ContestID      uuid.UUID
	RegistrationID uuid.UUID
	// NewRegistration registers another participant for the same contest
	// and returns the registration.
	NewRegistration func() uuid.UUID
	// NewContest creates another contest and returns its identifier.
	NewContest func() uuid.UUID
	// Now is what the submission store's clock reads, which is what an
	// answer's deadline is checked against.
	Now func() time.Time
}

// SequentialGateContract is what every contests.SequentialGate must do, run as
// subtests against one implementation. Both the in-memory SequentialProgress
// and postgres.Sequence run it, so the gate the service tests trust and the
// gate production uses are held to the same answers: a rule the fake got
// wrong would otherwise pass every service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repositories with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here.
func SequentialGateContract(t *testing.T, each func(t *testing.T, run func(context.Context, SequenceTarget))) {
	type shape struct {
		contest     uuid.UUID
		hidden      bool
		maxAttempts *int
	}
	create := func(t *testing.T, ctx context.Context, target SequenceTarget, s shape) contests.Question {
		t.Helper()
		if s.contest == uuid.Nil {
			s.contest = target.ContestID
		}
		q, err := target.Questions.Create(ctx, contests.Question{
			ContestID: s.contest, Kind: contests.KindText, Points: 5,
			MaxAttempts: s.maxAttempts, IsVisible: !s.hidden,
		})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		return q
	}
	question := func(t *testing.T, ctx context.Context, target SequenceTarget) contests.Question {
		t.Helper()
		return create(t, ctx, target, shape{})
	}
	capped := func(t *testing.T, ctx context.Context, target SequenceTarget, max int) contests.Question {
		t.Helper()
		return create(t, ctx, target, shape{maxAttempts: &max})
	}
	// answer records one answer the way Submit does, carrying the question's
	// own cap so the write is refused exactly where Submit's would be.
	answer := func(t *testing.T, ctx context.Context, target SequenceTarget, registration uuid.UUID, q contests.Question, correct bool) {
		t.Helper()
		if _, err := target.Submissions.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registration,
			QuestionID:     q.ID,
			Value:          "an answer",
			IsCorrect:      correct,
			Points:         q.Points,
			MaxAttempts:    q.MaxAttempts,
			Deadline:       target.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}
	}
	open := func(t *testing.T, ctx context.Context, target SequenceTarget, registration uuid.UUID, contest uuid.UUID, ord int) bool {
		t.Helper()
		got, err := target.Gate.Open(ctx, contest, registration, ord)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		return got
	}
	frontier := func(t *testing.T, ctx context.Context, target SequenceTarget, registration uuid.UUID, contest uuid.UUID) uuid.UUID {
		t.Helper()
		got, err := target.Gate.Frontier(ctx, contest, registration)
		if err != nil {
			t.Fatalf("Frontier() = %v", err)
		}
		return got
	}
	// expectOpen and expectFrontier ask on behalf of the target's own
	// participant in the target's own contest.
	expectOpen := func(t *testing.T, ctx context.Context, target SequenceTarget, ord int, want bool, why string) {
		t.Helper()
		if got := open(t, ctx, target, target.RegistrationID, target.ContestID, ord); got != want {
			t.Errorf("Open(ord %d) = %v, want %v: %s", ord, got, want, why)
		}
	}
	expectFrontier := func(t *testing.T, ctx context.Context, target SequenceTarget, want uuid.UUID, why string) {
		t.Helper()
		if got := frontier(t, ctx, target, target.RegistrationID, target.ContestID); got != want {
			t.Errorf("Frontier() = %s, want %s: %s", got, want, why)
		}
	}

	t.Run("a contest with no questions has no frontier", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			expectFrontier(t, ctx, target, uuid.Nil, "there is no question to point at")
			expectOpen(t, ctx, target, 1, true, "nothing precedes the first position")
		})
	})

	t.Run("the first question is open before anything is answered", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)

			expectOpen(t, ctx, target, q1.Ord, true, "nothing precedes the first question")
			expectOpen(t, ctx, target, q2.Ord, false, "question 1 has no answer yet")
			expectFrontier(t, ctx, target, q1.ID, "the first question is the lowest one not closed")
		})
	})

	t.Run("a correct answer closes a question", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)
			q3 := question(t, ctx, target)
			answer(t, ctx, target, target.RegistrationID, q1, true)

			expectOpen(t, ctx, target, q2.Ord, true, "question 1 was answered correctly, and question 2 itself does not count")
			expectOpen(t, ctx, target, q3.Ord, false, "question 2 is still unanswered")
			expectFrontier(t, ctx, target, q2.ID, "question 1 is closed")
		})
	})

	t.Run("spending every attempt closes a question", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := capped(t, ctx, target, 2)
			q2 := question(t, ctx, target)

			answer(t, ctx, target, target.RegistrationID, q1, false)
			expectOpen(t, ctx, target, q2.Ord, false, "one of question 1's two attempts is left")
			expectFrontier(t, ctx, target, q1.ID, "one of question 1's two attempts is left")

			answer(t, ctx, target, target.RegistrationID, q1, false)
			expectOpen(t, ctx, target, q2.Ord, true, "both of question 1's attempts are spent")
			expectFrontier(t, ctx, target, q2.ID, "both of question 1's attempts are spent")
		})
	})

	t.Run("wrong answers never close a question without a cap", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)
			for range 3 {
				answer(t, ctx, target, target.RegistrationID, q1, false)
			}

			expectOpen(t, ctx, target, q2.Ord, false, "question 1 has no cap and no correct answer")
			expectFrontier(t, ctx, target, q1.ID, "question 1 has no cap and no correct answer")
		})
	})

	t.Run("every question before the target must be closed, not only the last", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)
			q3 := question(t, ctx, target)
			answer(t, ctx, target, target.RegistrationID, q2, true)

			expectOpen(t, ctx, target, q3.Ord, false, "question 1 is still unanswered")
			expectFrontier(t, ctx, target, q1.ID, "question 1 is the lowest one not closed, whatever follows it")

			answer(t, ctx, target, target.RegistrationID, q1, true)
			expectOpen(t, ctx, target, q3.Ord, true, "questions 1 and 2 are both closed")
			expectFrontier(t, ctx, target, q3.ID, "questions 1 and 2 are both closed")
		})
	})

	t.Run("the frontier is nil once every question is closed", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := capped(t, ctx, target, 1)
			answer(t, ctx, target, target.RegistrationID, q1, true)
			answer(t, ctx, target, target.RegistrationID, q2, false)

			expectFrontier(t, ctx, target, uuid.Nil, "one question is answered and the other's only attempt is spent")
		})
	})

	t.Run("a hidden question counts like a visible one", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			hidden := create(t, ctx, target, shape{hidden: true})
			visible := question(t, ctx, target)

			expectOpen(t, ctx, target, visible.Ord, false, "the hidden question before it has no answer yet")
			expectFrontier(t, ctx, target, hidden.ID, "the hidden question is first in order")

			answer(t, ctx, target, target.RegistrationID, hidden, true)
			expectOpen(t, ctx, target, visible.Ord, true, "the hidden question is now closed")
			expectFrontier(t, ctx, target, visible.ID, "the hidden question is now closed")
		})
	})

	t.Run("follows display order, not the order questions were created in", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			created := question(t, ctx, target)
			moved := question(t, ctx, target)
			if err := target.Questions.Reorder(ctx, target.ContestID, []uuid.UUID{moved.ID, created.ID}); err != nil {
				t.Fatalf("Reorder() = %v", err)
			}

			// Reorder numbers the questions 1..n in the order given.
			expectFrontier(t, ctx, target, moved.ID, "the question moved to the front comes first")
			expectOpen(t, ctx, target, 2, false, "the question moved to the front has no answer yet")

			answer(t, ctx, target, target.RegistrationID, moved, true)
			expectFrontier(t, ctx, target, created.ID, "the question now first is closed")
			expectOpen(t, ctx, target, 2, true, "the question now first is closed")
		})
	})

	t.Run("another participant's answers close nothing for this one", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)
			other := target.NewRegistration()
			answer(t, ctx, target, other, q1, true)

			expectOpen(t, ctx, target, q2.Ord, false, "only the other participant answered question 1")
			expectFrontier(t, ctx, target, q1.ID, "only the other participant answered question 1")

			if !open(t, ctx, target, other, target.ContestID, q2.Ord) {
				t.Error("the other participant: Open(question 2) = false, want true: they answered question 1")
			}
			if got := frontier(t, ctx, target, other, target.ContestID); got != q2.ID {
				t.Errorf("the other participant: Frontier() = %s, want question 2 %s", got, q2.ID)
			}
		})
	})

	t.Run("another contest's questions do not count", func(t *testing.T) {
		each(t, func(ctx context.Context, target SequenceTarget) {
			elsewhere := create(t, ctx, target, shape{contest: target.NewContest()})
			q1 := question(t, ctx, target)
			q2 := question(t, ctx, target)
			answer(t, ctx, target, target.RegistrationID, q1, true)

			// The other contest's question holds position 1 there and is
			// unanswered, which would stand before question 2 here were the
			// contests not kept apart.
			if elsewhere.Ord != 1 {
				t.Fatalf("the other contest's question has position %d, want 1", elsewhere.Ord)
			}
			expectOpen(t, ctx, target, q2.Ord, true, "this contest's question 1 is closed")
			expectFrontier(t, ctx, target, q2.ID, "this contest's question 1 is closed")
		})
	})
}
