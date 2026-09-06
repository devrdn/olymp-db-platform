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

// Insert writes one submission row, computing its own attempt number,
// checking req.Deadline, enforcing "closed" (already correct, or every
// attempt spent) and computing the penalty-adjusted points_awarded (§6.1.1)
// all in the same statement — no count read first, no clock read first, and
// nothing here can be exceeded by two callers racing each other (finding 3,
// §8, finding 4, finding 5; see contests.SubmissionRepository's own doc).
//
// points_awarded is CASE WHEN is_correct THEN GREATEST(0, req.Points -
// req.PenaltyPerAttempt * <attempts already committed>) ELSE 0 END — the same
// COALESCE(MAX(s.attempt_no), 0) the attempt number itself is built from, so
// the penalty is always charged against exactly the attempts that landed
// before this one, never a count read a moment apart from the one that
// numbered this attempt. GREATEST(0, …) is §6.1.1's floor: a question can
// never take a participant below zero, and once it reaches zero every
// further wrong attempt already scored nothing (is_correct = false takes the
// ELSE branch) while a further correct one still floors at zero rather than
// going negative — which is what makes "remaining attempts are free" true by
// construction rather than a case this statement has to special-case.
//
// now() here is PostgreSQL's own clock, not time.Now() read in this process
// (§8) — and, because there is no explicit transaction wrapped around this
// one statement for the common case (contests.Service.submitOnce opens one
// only when a score update must land atomically with the write), now() is
// this statement's own execution time rather than a value pinned at some
// earlier BEGIN. That is what removes the gap finding 4 describes: the
// deadline is compared against the database's clock at the very moment the
// row is written, not at a moment read earlier and carried into a separate
// comparison.
//
// Filtered by (registration_id, question_id), the leading two columns of the
// table's own UNIQUE (registration_id, question_id, attempt_no) constraint —
// the same index CLAUDE.md rule 7 asks a filter to come with already exists
// for this one, the same reasoning postgres.Attempts.ForRegistration gives
// for its own query over this table. No migration is owed alongside this
// file.
//
// The correctness argument for the race, spelled out because it is not
// obvious from the SQL alone: every statement's HAVING clause is evaluated
// against whatever is already committed at the moment this statement began
// (READ COMMITTED takes one snapshot per statement) — never against another
// transaction's still-uncommitted insert. So a statement can only ever
// attempt to write when the *already-committed* count was strictly below
// MaxAttempts, and every successful write increases that committed count by
// exactly one attempt number, always the next integer after the highest one
// already committed (COALESCE(MAX(attempt_no), 0) + 1). Two statements that
// both compute the same next number from the same pre-commit snapshot are
// exactly the case the table's UNIQUE constraint exists for: PostgreSQL
// blocks the second at the point of insertion until the first's transaction
// resolves, and once it commits the second fails with a unique violation
// (mapped below to contests.ErrAttemptConflict) rather than silently
// succeeding at a number already taken. Put together: the attempt numbers
// that ever land for one (registration, question) pair are always exactly
// {1, ..., N} with no gaps and no duplicates, and N can never exceed
// MaxAttempts — a property that holds under any interleaving, not only the
// ones a test happens to schedule.
func (r *Submissions) Insert(ctx context.Context, req contests.SubmissionRequest) (contests.Submission, error) {
	row := r.querier(ctx).QueryRow(ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		SELECT $1, $2, COALESCE(MAX(s.attempt_no), 0) + 1, $3, $4,
		       CASE WHEN $4 THEN GREATEST(0, $5::int - COALESCE(MAX(s.attempt_no), 0) * $6::int) ELSE 0 END,
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
		// The HAVING clause produced no row for one of two reasons this one
		// statement cannot itself tell apart, and only refusals pay for
		// telling them apart (the common, successful case never reaches
		// here): explainRefusal asks a second, cheap question — no table
		// scan, just a comparison against now() — to decide which.
		return contests.Submission{}, r.explainRefusal(ctx, req.Deadline)
	case err != nil:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			// Lost the race for this attempt number to a concurrent insert
			// that committed first (see the doc above) — the caller retries
			// from a fresh read (contests.Service.Submit), never this method
			// reading the count and writing again itself.
			return contests.Submission{}, contests.ErrAttemptConflict
		}
		return contests.Submission{}, fmt.Errorf("insert submission: %w", err)
	}
	return out, nil
}

// explainRefusal decides, only once Insert already knows nothing was
// written, whether that was because the deadline had passed or because the
// question was already closed (already answered correctly, or every attempt
// spent) — the same priority Insert's own HAVING clause would give the
// deadline if it could report a reason directly, and the same one this
// codebase used before the two checks were folded into one statement.
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
