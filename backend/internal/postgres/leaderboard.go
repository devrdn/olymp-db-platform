package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

var _ leaderboard.Repository = (*Leaderboard)(nil)

// Leaderboard reads a contest's standings and records a reveal.
type Leaderboard struct {
	pool *pgxpool.Pool
}

// NewLeaderboard returns the leaderboard repository.
func NewLeaderboard(pool *pgxpool.Pool) *Leaderboard {
	return &Leaderboard{pool: pool}
}

func (r *Leaderboard) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// Standings aggregates every registration's submissions up to the cutoff.
//
// The cutoff is exclusive and applies to registrations too. The order is the
// one leaderboard.Rank puts rows in — the winner first in winner mode, then
// points, then the moment the score was reached, then the id — so LIMIT cuts
// below the top of the table, never through it. Rank sorts again; this order
// exists so the cut is right, not so the caller can skip sorting.
//
// submissions_registration_submitted_idx (migration 30) serves the join and
// the time filter, and carries the three columns the aggregate reads.
func (r *Leaderboard) Standings(ctx context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH scored AS (
			SELECT r.id,
			       u.login,
			       u.full_name,
			       u.status = 'deleted'                                             AS account_deleted,
			       r.status = 'disqualified'                                        AS disqualified,
			       COALESCE(SUM(s.points_awarded), 0)::int                          AS points,
			       (COUNT(DISTINCT s.question_id) FILTER (WHERE s.is_correct))::int AS solved,
			       MAX(s.submitted_at) FILTER (WHERE s.points_awarded > 0)          AS last_scored_at,
			       MIN(s.submitted_at) FILTER (WHERE s.is_correct AND q.kind = 'final') AS final_at
			FROM registrations r
			JOIN users u ON u.id = r.user_id
			LEFT JOIN submissions s ON s.registration_id = r.id AND s.submitted_at < $2
			LEFT JOIN questions q ON q.id = s.question_id
			WHERE r.contest_id = $1
			  AND r.created_at < $2
			  AND ($3 OR r.status <> 'disqualified')
			GROUP BY r.id, u.login, u.full_name, u.status, r.status
		), winner AS (
			SELECT id FROM scored WHERE final_at IS NOT NULL ORDER BY final_at, id LIMIT 1
		)
		SELECT s.id, s.login, s.full_name, s.account_deleted, s.disqualified,
		       s.points, s.solved, s.last_scored_at, s.final_at
		FROM scored s
		ORDER BY ($4 = 'winner' AND s.id IN (SELECT id FROM winner)) DESC,
		         s.points DESC, s.last_scored_at ASC NULLS LAST, s.id
		LIMIT $5`,
		q.ContestID, q.Cutoff, q.IncludeDisqualified, q.Scoring, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("read standings: %w", err)
	}
	defer rows.Close()

	var entries []leaderboard.Entry
	for rows.Next() {
		var e leaderboard.Entry
		if err := rows.Scan(&e.Registration, &e.Login, &e.FullName, &e.AccountDeleted, &e.Disqualified,
			&e.Points, &e.Solved, &e.LastScoredAt, &e.FinalAt); err != nil {
			return nil, fmt.Errorf("scan standings: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read standings: %w", err)
	}
	return entries, nil
}

// MarkRevealed sets leaderboard_revealed_at once and reports the moment in
// force.
//
// The update is conditional, so two organisers pressing the button together
// cannot overwrite each other's moment. It deliberately does not touch
// updated_at: the reclaim sweep measures a finished contest's grace period
// from that column (reclaimDeadline), and a reveal must not extend how long a
// contest's databases are kept.
func (r *Leaderboard) MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error) {
	var (
		revealedAt time.Time
		newly      bool
	)
	err := r.querier(ctx).QueryRow(ctx, `
		WITH updated AS (
			UPDATE contests SET leaderboard_revealed_at = $2
			WHERE id = $1 AND leaderboard_revealed_at IS NULL
			RETURNING leaderboard_revealed_at
		)
		SELECT COALESCE((SELECT leaderboard_revealed_at FROM updated), c.leaderboard_revealed_at),
		       EXISTS (SELECT 1 FROM updated)
		FROM contests c WHERE c.id = $1`,
		contestID, at).Scan(&revealedAt, &newly)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, contests.ErrNotFound
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mark the leaderboard revealed: %w", err)
	}
	return revealedAt, newly, nil
}
