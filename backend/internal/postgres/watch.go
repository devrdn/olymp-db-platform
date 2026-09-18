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

// Watch is the organiser's read side of monitoring (design §4): the
// participants table with its flags, the feed merged from every journal,
// the queries, the answers with the queries that led to them, and the
// workspace with its history. It writes nothing; Monitor, beside it, is the
// write side.
//
// Every read is a range over an index on the registration or on the contest
// (see watch_explain_test.go, which proves the plans), never a scan of a
// journal: query_log, submissions, participant_events and audit_log are the
// largest tables in the database.
//
// The interfaces over this type are declared by its consumer,
// monitor.WatchService (CLAUDE.md, Go layout rule 3).
type Watch struct {
	pool *pgxpool.Pool
}

// NewWatch returns the organiser's read side over pool.
func NewWatch(pool *pgxpool.Pool) *Watch { return &Watch{pool: pool} }

func (w *Watch) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, w.pool)
}

// rosterSQL is the whole participants table in one statement.
//
// The registrations of the contest come first and are bounded ($2); every
// counter is then a LATERAL aggregate over that one registration's own index
// range — query_log (registration_id, executed_at), submissions
// (registration_id, submitted_at), participant_events (registration_id, …) —
// so the cost grows with what this contest's participants did and nothing
// else.
//
// The identical-queries count needs the whole contest at once: the
// fingerprints of every participant's successful queries whose normalised
// text is at least $3 characters (the raw length is checked first, as a cheap
// bound on the normalised one, which can only be shorter), then those
// fingerprints more than one registration shares. Two statements with the
// same fingerprint have the same normalised text, so the length need not be
// checked on both sides.
//
// A correct answer is "blind" when no successful query of the same
// participant ran between their previous answer to any question (or the
// beginning) and this one — design §3's window, and §5's flag.
const rosterSQL = `
WITH regs AS (
    SELECT r.id, r.user_id, u.login, u.full_name, r.status, r.started_at, r.finished_at
    FROM registrations r
    JOIN users u ON u.id = r.user_id
    WHERE r.contest_id = $1
    ORDER BY u.login, r.id
    LIMIT $2
), long_ok AS (
    SELECT DISTINCT q.registration_id, q.sql_fingerprint
    FROM regs
    JOIN query_log q ON q.registration_id = regs.id
    WHERE q.status = 'ok'
      AND q.sql_fingerprint IS NOT NULL
      AND char_length(q.sql_text) >= $3
      AND char_length(regexp_replace(btrim(q.sql_text), '\s+', ' ', 'g')) >= $3
), shared AS (
    SELECT registration_id, count(*) AS identical
    FROM long_ok
    WHERE sql_fingerprint IN (
        SELECT sql_fingerprint FROM long_ok GROUP BY sql_fingerprint HAVING count(*) > 1)
    GROUP BY registration_id
)
SELECT regs.id, regs.user_id, regs.login, regs.full_name, regs.status, regs.started_at, regs.finished_at,
       ql.total, ql.errors, ql.rejected, ql.addresses,
       sb.correct, sb.wrong, sb.blind,
       ev.page_left, ev.away_ms, ev.pastes, ev.large_pastes, ev.ip_changes, ev.parallel,
       COALESCE(shared.identical, 0),
       greatest(ql.last_at, sb.last_at, ev.last_at)
FROM regs
LEFT JOIN shared ON shared.registration_id = regs.id
CROSS JOIN LATERAL (
    SELECT count(*) AS total,
           count(*) FILTER (WHERE status IN ('error', 'timeout')) AS errors,
           count(*) FILTER (WHERE status = 'rejected') AS rejected,
           count(DISTINCT ip) AS addresses,
           max(executed_at) AS last_at
    FROM query_log
    WHERE registration_id = regs.id
) ql
CROSS JOIN LATERAL (
    SELECT count(*) FILTER (WHERE a.is_correct) AS correct,
           count(*) FILTER (WHERE NOT a.is_correct) AS wrong,
           count(*) FILTER (WHERE a.is_correct AND NOT EXISTS (
               SELECT 1 FROM query_log q
               WHERE q.registration_id = regs.id
                 AND q.status = 'ok'
                 AND q.executed_at < a.submitted_at
                 AND (a.previous IS NULL OR q.executed_at >= a.previous))) AS blind,
           max(a.submitted_at) AS last_at
    FROM (
        SELECT s.is_correct, s.submitted_at,
               lag(s.submitted_at) OVER (ORDER BY s.submitted_at, s.id) AS previous
        FROM submissions s
        WHERE s.registration_id = regs.id
    ) a
) sb
CROSS JOIN LATERAL (
    SELECT count(*) FILTER (WHERE kind = 'page_left') AS page_left,
           COALESCE(sum((payload->>'away_ms')::bigint) FILTER (WHERE kind = 'page_left'), 0) AS away_ms,
           count(*) FILTER (WHERE kind = 'paste') AS pastes,
           count(*) FILTER (WHERE kind = 'paste'
                              AND payload->>'target' IN ('editor', 'answer')
                              AND (payload->>'chars')::bigint > $4) AS large_pastes,
           count(*) FILTER (WHERE kind = 'ip_changed') AS ip_changes,
           count(*) FILTER (WHERE kind = 'parallel_session') AS parallel,
           max(created_at) AS last_at
    FROM participant_events
    WHERE registration_id = regs.id
) ev
ORDER BY regs.login, regs.id`

// Roster computes the participants table of a contest, at most limit rows
// in login order, and whether there were more.
func (w *Watch) Roster(ctx context.Context, contest uuid.UUID, limit int) (monitor.Roster, error) {
	rows, err := w.querier(ctx).Query(ctx, rosterSQL,
		contest, limit+1, monitor.IdenticalQueryMinChars, monitor.LargePasteChars)
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
			&r.PageLeft, &r.AwayMs, &r.Pastes, &r.LargePastes, &r.IPChanges, &r.ParallelSessions,
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
