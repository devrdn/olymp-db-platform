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
// (TestWatchReadsScanNoJournal proves the plans on representative data),
// never a scan of a journal: query_log, submissions, participant_events and audit_log are the
// largest tables in the database.
//
// The interfaces over this type are declared by its consumer,
// monitor.WatchService (CLAUDE.md, Go layout rule 3).
type Watch struct {
	pool *pgxpool.Pool
	// wrap, when set, wraps every querier the reads use. Only this package's
	// tests set it, to EXPLAIN exactly the statements the reads send.
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

// rosterSQL is the whole participants table in one statement.
//
// The registrations of the contest come first and are bounded ($2); every
// counter is then a LATERAL aggregate over that one registration's own index
// range — query_log (registration_id, executed_at), submissions
// (registration_id, submitted_at), participant_events (registration_id, …) —
// so the cost grows with what this contest's participants did and nothing
// else. An aggregate is what keeps each LATERAL a range per registration: a
// plain join the planner may turn into a hash join over the whole journal.
//
// The identical-queries count rides on the same pass over the query log:
// each registration's distinct fingerprints of successful queries, then the
// fingerprints more than one registration has. Only a statement of at least
// monitor.IdenticalQueryMinChars normalised characters has a fingerprint at
// all (monitor.ComparableFingerprint, applied as the row is written), so no
// text is measured here.
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
), counted AS (
    SELECT regs.*,
           ql.total, ql.errors, ql.rejected, ql.addresses, ql.fingerprints,
           sb.correct, sb.wrong, sb.blind,
           ev.page_left, ev.away_ms, ev.pastes, ev.large_pastes, ev.ip_changes, ev.parallel,
           greatest(ql.last_at, sb.last_at, ev.last_at) AS last_at
    FROM regs
    CROSS JOIN LATERAL (
        SELECT count(*) AS total,
               count(*) FILTER (WHERE status IN ('error', 'timeout')) AS errors,
               count(*) FILTER (WHERE status = 'rejected') AS rejected,
               count(DISTINCT ip) AS addresses,
               COALESCE(array_agg(DISTINCT sql_fingerprint) FILTER (
                   WHERE status = 'ok' AND sql_fingerprint IS NOT NULL),
                   '{}') AS fingerprints,
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
                     AND q.executed_at >= COALESCE(a.previous, '-infinity'))) AS blind,
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
               -- A paste folded from identical ones in a row (monitor.CleanBatch)
               -- counts as all of them.
               COALESCE(sum(COALESCE((payload->>'count')::bigint, 1)) FILTER (WHERE kind = 'paste'), 0) AS pastes,
               count(*) FILTER (WHERE kind = 'paste'
                                  AND payload->>'target' IN ('editor', 'answer')
                                  AND (payload->>'chars')::bigint > $3) AS large_pastes,
               count(*) FILTER (WHERE kind = 'ip_changed') AS ip_changes,
               count(*) FILTER (WHERE kind = 'parallel_session') AS parallel,
               max(created_at) AS last_at
        FROM participant_events
        WHERE registration_id = regs.id
    ) ev
), shared AS (
    SELECT fingerprint
    FROM counted CROSS JOIN LATERAL unnest(counted.fingerprints) AS fingerprint
    GROUP BY fingerprint
    HAVING count(*) > 1
)
SELECT id, user_id, login, full_name, status, started_at, finished_at,
       total, errors, rejected, addresses,
       correct, wrong, blind,
       page_left, away_ms, pastes, large_pastes, ip_changes, parallel,
       (SELECT count(*) FROM unnest(counted.fingerprints) AS own
        WHERE own IN (SELECT fingerprint FROM shared)),
       last_at
FROM counted
ORDER BY login, id`

// Roster computes the participants table of a contest, at most limit rows
// in login order, and whether there were more.
func (w *Watch) Roster(ctx context.Context, contest uuid.UUID, limit int) (monitor.Roster, error) {
	rows, err := w.querier(ctx).Query(ctx, rosterSQL,
		contest, limit+1, monitor.LargePasteChars)
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
