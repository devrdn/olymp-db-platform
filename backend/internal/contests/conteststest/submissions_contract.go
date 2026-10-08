package conteststest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// SubmissionTarget is what one case of the contract runs against: a
// repository holding no submissions yet, a registration and question it may
// write to, and the store's own clock.
type SubmissionTarget struct {
	Repo           contests.SubmissionRepository
	RegistrationID uuid.UUID
	QuestionID     uuid.UUID
	// Now is what the store's clock reads when Insert runs. The deadline is
	// checked against that clock and not the caller's (SubmissionRequest's
	// own doc), so the contract can only state the boundary in its terms.
	Now func() time.Time
}

// SubmissionRepositoryContract is what every contests.SubmissionRepository
// must do, run as subtests against one implementation. Both the in-memory
// Submissions and postgres.Submissions run it, so the store the service
// tests trust and the store production uses are held to the same answers:
// a rule the fake got wrong would otherwise pass every service test and
// fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here; the race between two
// transactions is a property of the real statement and is proven against
// PostgreSQL alone.
func SubmissionRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, SubmissionTarget))) {
	// Every deadline is stated against the store's clock: a day ahead for
	// the cases not about the deadline, an hour behind for those that are.
	request := func(target SubmissionTarget, value string) contests.SubmissionRequest {
		return contests.SubmissionRequest{
			RegistrationID: target.RegistrationID,
			QuestionID:     target.QuestionID,
			Value:          value,
			Deadline:       target.Now().Add(24 * time.Hour),
		}
	}
	past := func(target SubmissionTarget) time.Time { return target.Now().Add(-time.Hour) }
	insert := func(t *testing.T, ctx context.Context, target SubmissionTarget, req contests.SubmissionRequest) contests.Submission {
		t.Helper()
		s, err := target.Repo.Insert(ctx, req)
		if err != nil {
			t.Fatalf("Insert(%q) = %v", req.Value, err)
		}
		return s
	}
	refused := func(t *testing.T, ctx context.Context, target SubmissionTarget, req contests.SubmissionRequest, want error) {
		t.Helper()
		if _, err := target.Repo.Insert(ctx, req); !errors.Is(err, want) {
			t.Fatalf("Insert(%q) error = %v, want %v", req.Value, err, want)
		}
	}

	t.Run("returns the row it wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			req := request(target, "the gardener")
			req.IsCorrect = true
			req.Points = 7
			got := insert(t, ctx, target, req)

			if got.ID == uuid.Nil {
				t.Error("ID is nil")
			}
			if got.RegistrationID != target.RegistrationID || got.QuestionID != target.QuestionID {
				t.Errorf("written for (%s, %s), want (%s, %s)",
					got.RegistrationID, got.QuestionID, target.RegistrationID, target.QuestionID)
			}
			if got.Value != "the gardener" || !got.IsCorrect || got.PointsAwarded != 7 || got.AttemptNo != 1 {
				t.Errorf("got %+v, want value %q, correct, 7 points, attempt 1", got, "the gardener")
			}
			if want := target.Now(); !got.SubmittedAt.Equal(want) {
				t.Errorf("SubmittedAt = %v, want the store's own clock, %v", got.SubmittedAt, want)
			}
		})
	})

	t.Run("takes the next attempt number", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			for want := 1; want <= 3; want++ {
				if got := insert(t, ctx, target, request(target, "wrong")).AttemptNo; got != want {
					t.Fatalf("AttemptNo = %d, want %d", got, want)
				}
			}
		})
	})

	t.Run("refuses once answered correctly", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			correct := request(target, "correct")
			correct.IsCorrect = true
			insert(t, ctx, target, correct)

			refused(t, ctx, target, request(target, "again"), contests.ErrQuestionClosed)
		})
	})

	t.Run("refuses once every attempt is spent", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			max := 2
			req := request(target, "wrong")
			req.MaxAttempts = &max
			insert(t, ctx, target, req)
			insert(t, ctx, target, req)

			refused(t, ctx, target, req, contests.ErrQuestionClosed)
		})
	})

	t.Run("a raised cap opens the question again", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			one, two := 1, 2
			req := request(target, "wrong")
			req.MaxAttempts = &one
			insert(t, ctx, target, req)
			refused(t, ctx, target, req, contests.ErrQuestionClosed)

			// The cap is the question's setting at the moment of writing,
			// never one a submission remembers (SubmissionRequest's own doc).
			req.MaxAttempts = &two
			if got := insert(t, ctx, target, req).AttemptNo; got != 2 {
				t.Fatalf("AttemptNo = %d, want 2", got)
			}
		})
	})

	t.Run("refuses after the deadline and writes nothing", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			late := request(target, "too late")
			late.Deadline = past(target)
			refused(t, ctx, target, late, contests.ErrDeadlinePassed)

			// Had the refused row been written, this would be attempt 2.
			if got := insert(t, ctx, target, request(target, "in time")).AttemptNo; got != 1 {
				t.Fatalf("AttemptNo after a refused late answer = %d, want 1", got)
			}
		})
	})

	t.Run("refuses at the deadline itself, not only after it", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			// Microseconds, the finest instant PostgreSQL stores.
			at := target.Now().Truncate(time.Microsecond)
			onTime := request(target, "just in time")
			onTime.Deadline = at
			refused(t, ctx, target, onTime, contests.ErrDeadlinePassed)

			onTime.Deadline = at.Add(time.Microsecond)
			insert(t, ctx, target, onTime)
		})
	})

	t.Run("prefers the deadline over a closed question", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			correct := request(target, "correct")
			correct.IsCorrect = true
			insert(t, ctx, target, correct)

			late := request(target, "too late as well")
			late.Deadline = past(target)
			refused(t, ctx, target, late, contests.ErrDeadlinePassed)
		})
	})

	t.Run("takes the penalty from the attempts already committed", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			wrong := request(target, "wrong")
			wrong.Points, wrong.PenaltyPerAttempt = 10, 3
			insert(t, ctx, target, wrong)
			insert(t, ctx, target, wrong)

			correct := wrong
			correct.Value, correct.IsCorrect = "correct", true
			if got := insert(t, ctx, target, correct).PointsAwarded; got != 4 {
				t.Fatalf("PointsAwarded = %d, want 4 (10 - 2*3)", got)
			}
		})
	})

	t.Run("never awards less than zero", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			wrong := request(target, "wrong")
			wrong.Points, wrong.PenaltyPerAttempt = 10, 6
			insert(t, ctx, target, wrong)
			insert(t, ctx, target, wrong)

			correct := wrong
			correct.Value, correct.IsCorrect = "correct", true
			if got := insert(t, ctx, target, correct).PointsAwarded; got != 0 {
				t.Fatalf("PointsAwarded = %d, want 0 (10 - 2*6, floored)", got)
			}
		})
	})

	t.Run("never awards points for a wrong answer", func(t *testing.T) {
		each(t, func(ctx context.Context, target SubmissionTarget) {
			wrong := request(target, "wrong")
			wrong.Points = 10
			if got := insert(t, ctx, target, wrong).PointsAwarded; got != 0 {
				t.Fatalf("PointsAwarded = %d, want 0", got)
			}
		})
	})
}
