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
// The participants table is the one read that touches no journal at all. A
// range is the right shape for a page, which returns what it reads; it is the
// wrong shape for a counter, which reads a contest's whole history to return
// one number and is asked for it again three seconds later. Since migration
// 000037 the journals keep those counters as they are written and Roster
// reads the summary — see rosterSQL, and registration_activity for what is
// kept and why each counter is kept the way it is.
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

// rosterSQL is the whole participants table in one statement, and it reads no
// journal at all.
//
// Every counter it shows used to be a LATERAL aggregate over one
// registration's whole range of query_log, submissions and
// participant_events. That is a read whose cost grows with the contest's
// history, repeated every three seconds for as long as the screen is open
// (monitor.RosterCacheTTL), and three of the aggregates — the distinct
// addresses, the set of fingerprints and the blind-answer test — read columns
// no serving index carries, so each of those passes fetched every journal row
// from the heap on top of it.
//
// Since migration 000037 the journals keep the counters themselves, one
// summary row per registration (registration_activity), so the table is the
// contest's registrations and their summaries and nothing more: one range of
// registrations_contest_score_idx, one primary-key lookup per participant,
// and a
// cost that is the number of participants at the start of a contest and the
// same number at the end of it. TestWatchRosterCostsWhatItShows measures that
// it does not move when the history behind it grows.
//
// A registration that has done nothing yet has no summary row — the first
// thing it does creates one — so the join is an outer one and every counter
// reads as the zero it is.
const rosterSQL = `
SELECT r.id, r.user_id, u.login, u.full_name, r.status, r.started_at, r.finished_at,
       COALESCE(a.queries, 0), COALESCE(a.query_errors, 0), COALESCE(a.query_rejected, 0),
       COALESCE(a.addresses, 0),
       COALESCE(a.correct, 0), COALESCE(a.wrong, 0), COALESCE(a.blind, 0),
       COALESCE(a.page_left, 0), COALESCE(a.away_ms, 0), COALESCE(a.pastes, 0),
       COALESCE(a.max_paste_chars, 0), COALESCE(a.ip_changes, 0), COALESCE(a.parallel_sessions, 0),
       COALESCE(a.identical_queries, 0),
       greatest(a.last_query_at, a.last_answer_at, a.last_event_at)
FROM registrations r
JOIN users u ON u.id = r.user_id
LEFT JOIN registration_activity a ON a.registration_id = r.id
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
			&r.PageLeft, &r.AwayMs, &r.Pastes, &r.MaxPasteChars, &r.IPChanges, &r.ParallelSessions,
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
