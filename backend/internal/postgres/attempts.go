package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Attempts implements contests.AttemptStore.
var _ contests.AttemptStore = (*Attempts)(nil)

// Attempts reads what a participant has already tried against each question,
// from the submissions table. Submissions writes them.
type Attempts struct {
	pool *pgxpool.Pool
}

// NewAttempts returns the attempt reader.
func NewAttempts(pool *pgxpool.Pool) *Attempts {
	return &Attempts{pool: pool}
}

func (r *Attempts) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ForRegistration returns the registration's attempt stats keyed by question.
// registration_id leads the UNIQUE (registration_id, question_id, attempt_no)
// index, which serves the filter (CLAUDE.md rule 7).
func (r *Attempts) ForRegistration(ctx context.Context, registrationID uuid.UUID) (map[uuid.UUID]contests.AttemptStats, error) {
	// Only a correct submission carries non-zero points, so the sum is the
	// winning attempt's award, or zero.
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT question_id, COUNT(*), bool_or(is_correct), COALESCE(SUM(points_awarded), 0)
		FROM submissions
		WHERE registration_id = $1
		GROUP BY question_id`,
		registrationID)
	if err != nil {
		return nil, fmt.Errorf("read attempt stats for registration %s: %w", registrationID, err)
	}
	defer rows.Close()

	out := map[uuid.UUID]contests.AttemptStats{}
	for rows.Next() {
		var (
			questionID uuid.UUID
			stats      contests.AttemptStats
		)
		if err := rows.Scan(&questionID, &stats.Attempts, &stats.Correct, &stats.PointsAwarded); err != nil {
			return nil, fmt.Errorf("scan attempt stats for registration %s: %w", registrationID, err)
		}
		out[questionID] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attempt stats for registration %s: %w", registrationID, err)
	}
	return out, nil
}
