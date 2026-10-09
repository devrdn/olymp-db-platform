package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Answerable implements queryproxy.Answerable.
var _ queryproxy.Answerable = (*Answerable)(nil)

// Answerable answers whether a registration still has a question it could
// answer, which the SQL console checks before taking another query
// (queryproxy.ErrNothingLeftToAnswer).
type Answerable struct {
	pool *pgxpool.Pool
}

// NewAnswerable returns the Answerable reader.
func NewAnswerable(pool *pgxpool.Pool) *Answerable {
	return &Answerable{pool: pool}
}

func (r *Answerable) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// AnswerableLeft reports whether any visible question of contestID is still
// open to registrationID: not answered correctly and, if max_attempts is set,
// not out of attempts. This must agree with contests.Reader's isClosed and
// Sequence's predicate.
//
// It may only err towards open. Hidden questions are skipped, unlike in
// Sequence: nobody can submit one, so counting it would keep the console open
// forever. A visible question without a translation is counted, though the
// participant's list drops it. A contest with no visible question answers
// true: closing the console over nothing shown would misreport a configuration
// gap, or a contest still being authored, as the participant's progress.
//
// EXISTS stops at the first open question. The filters use the UNIQUE indexes
// (contest_id, ord) on questions and (registration_id, question_id,
// attempt_no) on submissions (CLAUDE.md rule 7).
func (r *Answerable) AnswerableLeft(ctx context.Context, contestID, registrationID uuid.UUID) (bool, error) {
	var left bool
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM questions q
			WHERE q.contest_id = $1 AND q.is_visible
			  AND NOT (
			      EXISTS (
			          SELECT 1 FROM submissions s
			          WHERE s.registration_id = $2 AND s.question_id = q.id AND s.is_correct
			      )
			      OR (
			          q.max_attempts IS NOT NULL
			          AND (SELECT COUNT(*) FROM submissions s2
			               WHERE s2.registration_id = $2 AND s2.question_id = q.id) >= q.max_attempts
			      )
			  )
		) OR NOT EXISTS (
			SELECT 1 FROM questions q2 WHERE q2.contest_id = $1 AND q2.is_visible
		)`, contestID, registrationID).Scan(&left)
	if err != nil {
		return false, fmt.Errorf("check what is still answerable in contest %s: %w", contestID, err)
	}
	return left, nil
}
