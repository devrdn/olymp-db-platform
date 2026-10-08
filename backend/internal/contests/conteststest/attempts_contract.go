package conteststest

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// AttemptTarget is what one case of the contract runs against: a store with
// nothing recorded yet, and the repositories that write what it reads. The
// real store derives its answer from the submissions table, so the contract
// arranges state the way production does — a question through
// QuestionRepository.Create, an answer through SubmissionRepository.Insert —
// and never through anything only a fake could offer.
type AttemptTarget struct {
	Store       contests.AttemptStore
	Questions   contests.QuestionRepository
	Submissions contests.SubmissionRepository
	// ContestID is the contest questions are created in, and RegistrationID
	// a participant registered for it.
	ContestID      uuid.UUID
	RegistrationID uuid.UUID
	// NewRegistration registers another participant for the same contest
	// and returns the registration.
	NewRegistration func() uuid.UUID
	// Now is what the submission store's clock reads, which is what an
	// answer's deadline is checked against.
	Now func() time.Time
}

// AttemptStoreContract is what every contests.AttemptStore must do, run as
// subtests against one implementation. Both the in-memory Attempts and
// postgres.Attempts run it, so the store the service tests trust and the store
// production uses are held to the same answers: a rule the fake got wrong
// would otherwise pass every service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repositories with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here.
func AttemptStoreContract(t *testing.T, each func(t *testing.T, run func(context.Context, AttemptTarget))) {
	question := func(t *testing.T, ctx context.Context, target AttemptTarget) contests.Question {
		t.Helper()
		q, err := target.Questions.Create(ctx, contests.Question{ContestID: target.ContestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		return q
	}
	// answer records one answer the way Submit does. A correct one is worth
	// points, less penalty for each attempt already made; a wrong one earns
	// nothing, whatever points it carries.
	answer := func(t *testing.T, ctx context.Context, target AttemptTarget, registration, questionID uuid.UUID, correct bool, points, penalty int) {
		t.Helper()
		if _, err := target.Submissions.Insert(ctx, contests.SubmissionRequest{
			RegistrationID:    registration,
			QuestionID:        questionID,
			Value:             "an answer",
			IsCorrect:         correct,
			Points:            points,
			PenaltyPerAttempt: penalty,
			Deadline:          target.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}
	}
	read := func(t *testing.T, ctx context.Context, target AttemptTarget, registration uuid.UUID) map[uuid.UUID]contests.AttemptStats {
		t.Helper()
		stats, err := target.Store.ForRegistration(ctx, registration)
		if err != nil {
			t.Fatalf("ForRegistration() = %v", err)
		}
		return stats
	}
	expect := func(t *testing.T, got, want map[uuid.UUID]contests.AttemptStats, who string) {
		t.Helper()
		same := len(got) == len(want)
		for id, stats := range want {
			if entry, ok := got[id]; !ok || entry != stats {
				same = false
			}
		}
		if !same {
			t.Errorf("%s: ForRegistration() = %+v, want %+v", who, got, want)
		}
	}

	t.Run("is empty for a registration that never answered", func(t *testing.T) {
		each(t, func(ctx context.Context, target AttemptTarget) {
			question(t, ctx, target)

			if got := read(t, ctx, target, target.RegistrationID); len(got) != 0 {
				t.Errorf("ForRegistration() = %+v, want no entries", got)
			}
		})
	})

	t.Run("counts attempts and correctness per question", func(t *testing.T) {
		each(t, func(ctx context.Context, target AttemptTarget) {
			missed := question(t, ctx, target)
			retried := question(t, ctx, target)
			first := question(t, ctx, target)
			untouched := question(t, ctx, target)

			answer(t, ctx, target, target.RegistrationID, missed.ID, false, 10, 2)
			answer(t, ctx, target, target.RegistrationID, missed.ID, false, 10, 2)
			answer(t, ctx, target, target.RegistrationID, retried.ID, false, 10, 2)
			answer(t, ctx, target, target.RegistrationID, retried.ID, true, 10, 2)
			answer(t, ctx, target, target.RegistrationID, first.ID, true, 5, 2)

			got := read(t, ctx, target, target.RegistrationID)
			expect(t, got, map[uuid.UUID]contests.AttemptStats{
				missed.ID: {Attempts: 2, Correct: false, PointsAwarded: 0},
				// The winning attempt's own award, 10 less one wrong
				// attempt's penalty of 2.
				retried.ID: {Attempts: 2, Correct: true, PointsAwarded: 8},
				first.ID:   {Attempts: 1, Correct: true, PointsAwarded: 5},
			}, "the participant")
			if stats, ok := got[untouched.ID]; ok {
				t.Errorf("a question never answered has an entry: %+v", stats)
			}
		})
	})

	t.Run("never counts another participant's attempts", func(t *testing.T) {
		each(t, func(ctx context.Context, target AttemptTarget) {
			q := question(t, ctx, target)
			other := target.NewRegistration()
			silent := target.NewRegistration()

			answer(t, ctx, target, target.RegistrationID, q.ID, true, 10, 0)
			answer(t, ctx, target, other, q.ID, false, 10, 0)
			answer(t, ctx, target, other, q.ID, false, 10, 0)

			expect(t, read(t, ctx, target, target.RegistrationID), map[uuid.UUID]contests.AttemptStats{
				q.ID: {Attempts: 1, Correct: true, PointsAwarded: 10},
			}, "the participant who answered once")
			expect(t, read(t, ctx, target, other), map[uuid.UUID]contests.AttemptStats{
				q.ID: {Attempts: 2, Correct: false, PointsAwarded: 0},
			}, "the participant who answered twice")
			if got := read(t, ctx, target, silent); len(got) != 0 {
				t.Errorf("a participant who never answered: ForRegistration() = %+v, want no entries", got)
			}
		})
	})

	t.Run("hands back a map the caller owns", func(t *testing.T) {
		each(t, func(ctx context.Context, target AttemptTarget) {
			q := question(t, ctx, target)
			answer(t, ctx, target, target.RegistrationID, q.ID, false, 10, 0)

			first := read(t, ctx, target, target.RegistrationID)
			first[q.ID] = contests.AttemptStats{Attempts: 99, Correct: true, PointsAwarded: 99}
			first[uuid.New()] = contests.AttemptStats{Attempts: 1}

			expect(t, read(t, ctx, target, target.RegistrationID), map[uuid.UUID]contests.AttemptStats{
				q.ID: {Attempts: 1, Correct: false, PointsAwarded: 0},
			}, "a second read after editing the first")
		})
	})
}
