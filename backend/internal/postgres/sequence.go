package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sequence implements contests.SequentialGate.
var _ contests.SequentialGate = (*Sequence)(nil)

// Sequence answers whether a question has opened yet in a sequential contest
// (§6.1.1), by reading questions and submissions — it never writes either.
type Sequence struct {
	pool *pgxpool.Pool
}

// NewSequence returns the sequential-progression reader.
func NewSequence(pool *pgxpool.Pool) *Sequence {
	return &Sequence{pool: pool}
}

func (r *Sequence) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// Open reports whether every question of contestID ordered strictly before
// ord is closed for registrationID: answered correctly, or every attempt
// spent.
//
// One statement, one round trip, regardless of how many questions the
// contest has: NOT EXISTS short-circuits at the first open (unclosed)
// question, and every subquery inside it is filtered by
// (registration_id, question_id) — the leading two columns of submissions'
// own UNIQUE (registration_id, question_id, attempt_no) constraint, the same
// index CLAUDE.md rule 7 asks a filter to come with (see
// postgres.Submissions.Insert's own doc for why that index already exists).
// questions.contest_id and questions.ord are covered the same way by the
// table's UNIQUE (contest_id, ord). What this costs scales with how many
// questions precede the target one in this contest, not with anything
// touching every registration or every contest in the installation.
func (r *Sequence) Open(ctx context.Context, contestID, registrationID uuid.UUID, ord int) (bool, error) {
	var open bool
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1
			FROM questions q
			WHERE q.contest_id = $1 AND q.ord < $2
			  AND NOT (
			      EXISTS (
			          SELECT 1 FROM submissions s
			          WHERE s.registration_id = $3 AND s.question_id = q.id AND s.is_correct
			      )
			      OR (
			          q.max_attempts IS NOT NULL
			          AND (SELECT COUNT(*) FROM submissions s2
			               WHERE s2.registration_id = $3 AND s2.question_id = q.id) >= q.max_attempts
			      )
			  )
		)`, contestID, ord, registrationID).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("check sequential progress for contest %s: %w", contestID, err)
	}
	return open, nil
}
