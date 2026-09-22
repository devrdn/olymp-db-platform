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

// Showcase implements showcase.Repository: the two reads the landing page
// makes, which anybody can make without signing in.
var _ showcase.Repository = (*Showcase)(nil)

// Showcase reads what an installation shows about itself.
//
// Both statements are aggregates over whole tables, which is only acceptable
// because of what they aggregate. Recent reads contests, whose row count is
// how many olympiads the installation has ever run — hundreds, not millions —
// so its selection and its ordering need no index of their own. Numbers
// touches neither journal: both the queries figure and the solved figure are
// sums over registration_activity, the summary migration 000037 keeps by
// trigger, which holds one row per registration rather than one per query and
// one per answer. Counting a journal here would put a scan of the two largest
// tables in the installation behind a page that anybody may load, from
// anywhere, without an account.
type Showcase struct {
	pool *pgxpool.Pool
}

// NewShowcase returns the landing page's read side over pool.
func NewShowcase(pool *pgxpool.Pool) *Showcase { return &Showcase{pool: pool} }

func (r *Showcase) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// Numbers counts the installation's four numbers in one statement.
//
// Contests are the ones that were actually held — finished and archived — so
// the figure cannot be raised by publishing something nobody sat. Queries and
// Solved are both sums of the summary counters, never count(*) over query_log
// or submissions (see the type's own doc), and both come off the same single
// pass over registration_activity.
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

// Recent returns the contests a visitor may see, newest first.
//
// The four statuses are the same four a participant's own catalogue lists
// (internal/postgres.Profile): a draft is nobody's business but its authors',
// and this list is read by people who are not signed in at all.
//
// The titles come with the row rather than from a second read per contest —
// the same trick the contest catalogue uses (contestColumns above) — because
// the visitor's language is chosen above this layer and one cached read has
// to serve every language at once. Ordered by the window the page shows, with
// the identifier breaking ties so that two contests starting in the same
// second do not swap places between two reads of the same list.
//
// That ordering is a sort of the whole selection, and it is meant to be. No
// existing index serves it — contests_status_starts_at_idx leads with status
// and orders by starts_at alone, and the page orders by starts_at falling
// back to created_at — so serving it would take an index of its own: the
// expression, descending, partial on the four public statuses. It is not
// worth one. The table holds one row per olympiad the installation has ever
// run, so the sort is over hundreds of rows, and the minute of cache in
// showcase.Service means it happens at most once a minute however many
// visitors arrive. Against that, an index here is a write on every contest
// an organiser creates or edits and one more thing a later change to the
// ordering has to remember. CLAUDE.md rule 7 asks for an index behind a
// filter the API offers, and this is neither: the selection is a fixed
// clause no caller can widen, and the caller chooses nothing about the order.
// Revisit it if the selection ever stops being the whole small table.
//
// The cover joins rather than being asked for per row. The page draws a card
// per contest and each card carries a picture, so the alternative is six
// reads of contest_covers behind a page anybody may load without an account;
// the join is over the table's own primary key and a contest without a row
// there is not a missing cover but a drawn one, which is why it is a LEFT
// join answering empty strings rather than a filter.
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
