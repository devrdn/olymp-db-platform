package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Submissions implements contests.SubmissionRepository.
var _ contests.SubmissionRepository = (*Submissions)(nil)

// Submissions records participants' answers.
type Submissions struct {
	pool *pgxpool.Pool
}

// NewSubmissions returns the submission repository.
func NewSubmissions(pool *pgxpool.Pool) *Submissions {
	return &Submissions{pool: pool}
}

func (r *Submissions) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// Insert writes one submission in a single statement that numbers the
// attempt, checks req.Deadline, refuses a closed question (already correct,
// or every attempt spent) and computes points_awarded. Nothing is read first,
// so concurrent callers cannot exceed the limits.
//
// The penalty is charged against the same COALESCE(MAX(attempt_no), 0) that
// numbers the attempt, floored at zero (§6.1.1). The product is computed in
// bigint because Points * PenaltyPerAttempt * attempts can overflow int4 at
// values contests.Question.Validate allows.
//
// The deadline is compared with the database's now(), never this process's
// clock. Inside contests.Service.submitOnce's transaction, now() is the
// transaction start; Insert is that transaction's first statement, so now()
// precedes the write by one round trip at most.
//
// The filter is served by the UNIQUE (registration_id, question_id,
// attempt_no) index (CLAUDE.md rule 7).
//
// Race safety: under READ COMMITTED the HAVING clause sees only committed
// rows, so a write happens only when the committed count is below
// MaxAttempts, and it takes the next number after the committed maximum. Two
// statements that compute the same number collide on the UNIQUE constraint;
// the second waits for the first and then fails with a unique violation,
// returned as contests.ErrAttemptConflict. Attempt numbers are therefore
// always 1..N with N <= MaxAttempts.
func (r *Submissions) Insert(ctx context.Context, req contests.SubmissionRequest) (contests.Submission, error) {
	row := r.querier(ctx).QueryRow(ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		SELECT $1, $2, COALESCE(MAX(s.attempt_no), 0) + 1, $3, $4,
		       CASE WHEN $4 THEN GREATEST(0, $5::int - COALESCE(MAX(s.attempt_no), 0)::bigint * $6::int) ELSE 0 END,
		       now()
		FROM submissions s
		WHERE s.registration_id = $1 AND s.question_id = $2
		HAVING COUNT(*) FILTER (WHERE s.is_correct) = 0
		   AND ($7::int IS NULL OR COUNT(*) < $7::int)
		   AND now() < $8::timestamptz
		RETURNING id, registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at`,
		req.RegistrationID, req.QuestionID, req.Value, req.IsCorrect, req.Points, req.PenaltyPerAttempt, req.MaxAttempts, req.Deadline)

	var out contests.Submission
	err := row.Scan(&out.ID, &out.RegistrationID, &out.QuestionID, &out.AttemptNo,
		&out.Value, &out.IsCorrect, &out.PointsAwarded, &out.SubmittedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The statement cannot say why HAVING refused; only refusals pay
		// for the second query that tells.
		return contests.Submission{}, r.explainRefusal(ctx, req.Deadline)
	case err != nil:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			// Lost the race for this attempt number; contests.Service.Submit
			// retries from a fresh read.
			return contests.Submission{}, contests.ErrAttemptConflict
		}
		return contests.Submission{}, fmt.Errorf("insert submission: %w", err)
	}
	return out, nil
}

// explainRefusal tells why Insert wrote nothing: a passed deadline takes
// priority over a closed question.
func (r *Submissions) explainRefusal(ctx context.Context, deadline time.Time) error {
	var passed bool
	if err := r.querier(ctx).QueryRow(ctx, `SELECT now() >= $1::timestamptz`, deadline).Scan(&passed); err != nil {
		return fmt.Errorf("check whether the deadline had passed: %w", err)
	}
	if passed {
		return contests.ErrDeadlinePassed
	}
	return contests.ErrQuestionClosed
}
