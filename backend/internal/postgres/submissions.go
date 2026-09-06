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

// Now returns the core database's own clock (§8). A SELECT of PostgreSQL's
// own now(), not time.Now() read in this process: the deadline guarantee
// submission.go builds on this must not depend on the application server's
// clock agreeing with the database's, only on the one clock the write itself
// lands by.
func (r *Submissions) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := r.querier(ctx).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("read the core database's clock: %w", err)
	}
	return now, nil
}

// Insert writes one submission row, computing its own attempt number and
// enforcing "closed" (already correct, or every attempt spent) in the same
// statement — no count read first, and nothing here can be exceeded by two
// callers racing each other (finding 3, docs/ARCHITECTURE.md §8, and see
// contests.SubmissionRepository's own doc).
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
		SELECT $1, $2, COALESCE(MAX(s.attempt_no), 0) + 1, $3, $4, $5, $6
		FROM submissions s
		WHERE s.registration_id = $1 AND s.question_id = $2
		HAVING COUNT(*) FILTER (WHERE s.is_correct) = 0
		   AND ($7::int IS NULL OR COUNT(*) < $7::int)
		RETURNING id, registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at`,
		req.RegistrationID, req.QuestionID, req.Value, req.IsCorrect, req.PointsAwarded, req.SubmittedAt, req.MaxAttempts)

	var out contests.Submission
	err := row.Scan(&out.ID, &out.RegistrationID, &out.QuestionID, &out.AttemptNo,
		&out.Value, &out.IsCorrect, &out.PointsAwarded, &out.SubmittedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The HAVING clause produced no row: this registration has already
		// answered the question correctly, or spent every attempt it had.
		return contests.Submission{}, contests.ErrQuestionClosed
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
