package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/showcase"
)

var _ showcase.Repository = (*Showcase)(nil)

// Showcase reads what the public landing page shows about the installation.
//
// Both statements aggregate whole tables, which is acceptable only because
// those tables are small: contests holds one row per olympiad, and the query
// and solved figures sum registration_activity (one row per registration,
// kept by trigger) instead of counting query_log or submissions, the largest
// tables, behind an anonymous page.
type Showcase struct {
	pool *pgxpool.Pool
}

// NewShowcase returns the landing page's read side over pool.
func NewShowcase(pool *pgxpool.Pool) *Showcase { return &Showcase{pool: pool} }

func (r *Showcase) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// Numbers counts the installation's four numbers in one statement. Only
// finished and archived contests count, so publishing something nobody sat
// cannot raise the figure.
func (r *Showcase) Numbers(ctx context.Context) (showcase.Numbers, error) {
	var n showcase.Numbers
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM contests WHERE status IN ('finished', 'archived')),
		       (SELECT count(*) FROM registrations),
		       a.queries, a.correct
		FROM (
		    SELECT COALESCE(sum(queries), 0) AS queries,
		           COALESCE(sum(correct), 0) AS correct
		    FROM registration_activity
		) a`).
		Scan(&n.Contests, &n.Participants, &n.Queries, &n.Solved)
	if err != nil {
		return showcase.Numbers{}, fmt.Errorf("count the showcase numbers: %w", err)
	}
	return n, nil
}

// Recent returns the public contests, newest first, with every language's
// title in the row so one cached read serves all languages. The id breaks
// ties so the order is stable between reads.
//
// The ORDER BY sorts the whole selection; no index serves it, on purpose. The
// table holds hundreds of rows, showcase.Service caches the result for a
// minute, and the filter is fixed, so CLAUDE.md rule 7 does not apply.
// Revisit if contests grows large.
//
// The cover is a LEFT JOIN on its primary key: a contest without one shows a
// drawn cover, answered as empty strings.
func (r *Showcase) Recent(ctx context.Context, limit int) ([]showcase.Contest, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT c.id, c.status, c.starts_at, c.ends_at,
		       (c.status IN ('finished', 'archived') OR c.leaderboard_revealed_at IS NOT NULL) AS table_open,
		       COALESCE(cc.hash, ''), COALESCE(cc.attribution, ''),
		       COALESCE((
		           SELECT json_object_agg(ct.lang, ct.title)
		           FROM contest_translations ct WHERE ct.contest_id = c.id
		       ), '{}'::json),
		       COALESCE((
		           SELECT cl.lang FROM contest_languages cl
		           WHERE cl.contest_id = c.id AND cl.is_default
		           ORDER BY cl.lang LIMIT 1
		       ), '')
		FROM contests c
		LEFT JOIN contest_covers cc ON cc.contest_id = c.id
		WHERE `+publicStatusFilter("c.status", 2)+`
		ORDER BY COALESCE(c.starts_at, c.created_at) DESC, c.id DESC
		LIMIT $1`, limit, contests.PublicStatuses)
	if err != nil {
		return nil, fmt.Errorf("read the recent contests: %w", err)
	}
	defer rows.Close()

	out := make([]showcase.Contest, 0, limit)
	for rows.Next() {
		var (
			c      showcase.Contest
			titles []byte
		)
		if err := rows.Scan(&c.ID, &c.Status, &c.StartsAt, &c.EndsAt, &c.TableOpen,
			&c.CoverHash, &c.CoverAttribution, &titles, &c.DefaultLanguage); err != nil {
			return nil, fmt.Errorf("scan a recent contest: %w", err)
		}
		if err := json.Unmarshal(titles, &c.Titles); err != nil {
			return nil, fmt.Errorf("decode the titles of contest %s: %w", c.ID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the recent contests: %w", err)
	}
	return out, nil
}
