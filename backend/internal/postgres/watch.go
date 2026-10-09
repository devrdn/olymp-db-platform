package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Watch is the organiser's read side of monitoring: the participants table,
// the merged feed, queries, answers and the workspace history. It writes
// nothing; Monitor is the write side.
//
// Every read is a range over an index on the registration or the contest,
// never a scan of a journal (query_log, submissions, participant_events,
// audit_log are the largest tables); TestWatchReadsScanNoJournal checks the
// plans. Roster reads no journal at all, only the counters the journals keep
// (see rosterSQL).
type Watch struct {
	pool *pgxpool.Pool
	// wrap, when set, wraps every querier the reads use, so tests can EXPLAIN
	// the statements sent.
	wrap func(storage.Querier) storage.Querier
}

// NewWatch returns the organiser's read side over pool.
func NewWatch(pool *pgxpool.Pool) *Watch { return &Watch{pool: pool} }

func (w *Watch) querier(ctx context.Context) storage.Querier {
	q := storage.QuerierFrom(ctx, w.pool)
	if w.wrap != nil {
		return w.wrap(q)
	}
	return q
}

// rosterSQL is the whole participants table in one statement. It reads no
// journal, only the per-registration counters in registration_activity, so
// its cost is one range of registrations_contest_score_idx plus one
// primary-key lookup per participant, however long the contest has run. A
// registration that has done nothing has no summary row, hence the outer join.
//
// Identical statements are not a counter: writing one would make a query lock
// another participant's row while holding its own, and two participants
// running the same statement at once would deadlock. So the write side only
// records who ran what (contest_query_fingerprints, one row per registration
// per distinct statement), and the count is derived here from one ordered
// index-only scan of its primary key (contest_id, fingerprint,
// registration_id), which needs no sort for the window.
const rosterSQL = `
WITH identical AS (
    SELECT held.registration_id, count(*) AS n
    FROM (
        SELECT registration_id, count(*) OVER (PARTITION BY fingerprint) AS holders
        FROM contest_query_fingerprints
        WHERE contest_id = $1
    ) held
    WHERE held.holders > 1
    GROUP BY held.registration_id
)
SELECT r.id, r.user_id, u.login, u.full_name, r.status, r.started_at, r.finished_at,
       COALESCE(a.queries, 0), COALESCE(a.query_errors, 0), COALESCE(a.query_rejected, 0),
       COALESCE(a.addresses, 0),
       COALESCE(a.correct, 0), COALESCE(a.wrong, 0), COALESCE(a.blind, 0),
       COALESCE(a.page_left, 0), COALESCE(a.away_ms, 0), COALESCE(a.pastes, 0),
       COALESCE(a.max_paste_chars, 0), COALESCE(a.ip_changes, 0), COALESCE(a.parallel_sessions, 0),
       COALESCE(i.n, 0),
       greatest(a.last_query_at, a.last_answer_at, a.last_event_at)
FROM registrations r
JOIN users u ON u.id = r.user_id
LEFT JOIN registration_activity a ON a.registration_id = r.id
LEFT JOIN identical i ON i.registration_id = r.id
WHERE r.contest_id = $1
ORDER BY u.login, r.id
LIMIT $2`

// Roster computes the participants table of a contest, at most limit rows
// in login order, and whether there were more.
func (w *Watch) Roster(ctx context.Context, contest uuid.UUID, limit int) (monitor.Roster, error) {
	rows, err := w.querier(ctx).Query(ctx, rosterSQL, contest, limit+1)
	if err != nil {
		return monitor.Roster{}, fmt.Errorf("compute the participants table of %s: %w", contest, err)
	}
	defer rows.Close()

	roster := monitor.Roster{GeneratedAt: time.Now(), Rows: []monitor.RosterRow{}}
	for rows.Next() {
		var r monitor.RosterRow
		if err := rows.Scan(&r.Registration, &r.User, &r.Login, &r.FullName, &r.Status, &r.StartedAt, &r.FinishedAt,
			&r.Queries, &r.QueryErrors, &r.QueryRejected, &r.Addresses,
			&r.Correct, &r.Wrong, &r.BlindCorrect,
			&r.PageLeft, &r.AwayMs, &r.Pastes, &r.LargestPasteChars, &r.IPChanges, &r.ParallelSessions,
			&r.IdenticalQueries, &r.LastActivity); err != nil {
			return monitor.Roster{}, fmt.Errorf("scan a participants table row: %w", err)
		}
		roster.Rows = append(roster.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return monitor.Roster{}, fmt.Errorf("compute the participants table of %s: %w", contest, err)
	}
	if len(roster.Rows) > limit {
		roster.Rows, roster.Truncated = roster.Rows[:limit], true
	}
	return roster, nil
}
