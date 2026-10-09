package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sequence implements contests.SequentialGate.
var _ contests.SequentialGate = (*Sequence)(nil)

// Sequence answers whether a question has opened yet in a sequential contest
// (§6.1.1). It only reads.
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
// NOT EXISTS stops at the first open question. The subqueries use the UNIQUE
// (registration_id, question_id, attempt_no) index on submissions and the
// UNIQUE (contest_id, ord) index on questions (CLAUDE.md rule 7), so the cost
// grows with the preceding questions only.
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

// Frontier returns the lowest-ord question of contestID that is not closed
// for registrationID, or uuid.Nil when all are closed. Hidden questions count,
// as in Open (§6.1.1). It uses the same indexes as Open.
func (r *Sequence) Frontier(ctx context.Context, contestID, registrationID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT q.id
		FROM questions q
		WHERE q.contest_id = $1
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
		ORDER BY q.ord
		LIMIT 1`, contestID, registrationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Every question is closed.
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find the open question for contest %s: %w", contestID, err)
	}
	return id, nil
}
