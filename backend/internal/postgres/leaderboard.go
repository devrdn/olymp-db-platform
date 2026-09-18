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

// Standings aggregates every registration's submissions up to the cutoff,
// for points and winner mode; ICPC has ICPCStandings.
//
// The cutoff is exclusive and applies to registrations too. The order is the
// one leaderboard.Rank puts rows in — the winner first in winner mode, then
// points, then the moment the score was reached, then the id — so LIMIT cuts
// below the top of the table, never through it. Rank sorts again; this order
// exists so the cut is right, not so the caller can skip sorting.
//
// submissions_registration_submitted_idx (migration 30, with the id added to
// its key by 33) serves the join and the time filter, and carries the three
// columns the aggregate reads.
func (r *Leaderboard) Standings(ctx context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	// An ICPC table here would come back ordered by points, all zero, with no
	// grid: refused before the query rather than served wrong.
	if q.Scoring == contests.ScoringICPC {
		return nil, errors.New("icpc standings are read by ICPCStandings, not Standings")
	}
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

// ICPCStandings computes every registration's ICPC row up to the cutoff — a
// cell per visible question in the questions' order, and from the cells the
// number solved, the penalty time and the last solve — and, in the same
// statement, the grid: how many visible questions there are and each one's
// earliest solve.
//
// A cell's solve is the first correct answer before the cutoff; its wrong
// count is the wrong answers before that solve, or before the cutoff when
// there is none. The solving minute is floor((solve - start) / 60 s), with the
// start taken from the contest in the query itself: the window's starts_at,
// or the participant's own started_at under an individual timer. The penalty
// and the contest's icpc_penalty_min are read here too, so no caller can pass
// the wrong one. A minute is never negative: starts_at stays editable while
// the contest runs, and moving it later must not pay anybody for solving
// "before" it.
//
// Pending attempts are counted only when the query asks (q.Pending, a frozen
// table): answers in [From, Until) on a question with no solve before the
// cutoff. Nothing else about those answers reaches the row.
//
// A question's earliest solve is taken over every registration made before
// the cutoff that is not disqualified, whether or not the query includes the
// disqualified and whatever LIMIT cuts, so a first-solver mark does not
// depend on which rows are returned. The width comes from the same visible
// questions the cells are built from, so the letters and the cells cannot
// disagree even while an organiser toggles a question's visibility.
//
// The order is leaderboard.RankICPC's — solved, penalty, last solve, id — so
// LIMIT cuts below the top of the table, never through it. The grid rides on
// the first row; a table with no rows still returns one row, carrying only
// the grid.
//
// Indexes: questions_contest_id_ord_key (contest_id, ord) reads the contest's
// questions in order; submissions_registration_submitted_idx (migration 30)
// serves both the cutoff and the pending window per registration, and its
// INCLUDE carries question_id and is_correct, the only other columns read.
// The attempt a question was solved with is derived from the wrong count
// rather than read from attempt_no, which the index does not carry.
func (r *Leaderboard) ICPCStandings(ctx context.Context, q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid, error) {
	var pendingFrom, pendingUntil *time.Time
	if q.Pending != nil {
		pendingFrom, pendingUntil = &q.Pending.From, &q.Pending.Until
	}
	rows, err := r.querier(ctx).Query(ctx, `
		WITH contest AS (
			SELECT c.timing, c.starts_at, c.icpc_penalty_min FROM contests c WHERE c.id = $1
		), visible AS (
			SELECT q.id, row_number() OVER (ORDER BY q.ord) AS pos
			FROM questions q
			WHERE q.contest_id = $1 AND q.is_visible
		), entrant AS (
			SELECT r.id, u.login, u.full_name,
			       u.status = 'deleted'      AS account_deleted,
			       r.status = 'disqualified' AS disqualified,
			       CASE WHEN c.timing = 'individual' THEN COALESCE(r.started_at, c.starts_at)
			            ELSE c.starts_at END AS start_at
			FROM registrations r
			JOIN users u ON u.id = r.user_id
			CROSS JOIN contest c
			WHERE r.contest_id = $1
			  AND r.created_at < $2
			  AND ($3 OR r.status <> 'disqualified')
		), answer AS (
			SELECT s.registration_id, s.question_id, s.submitted_at, s.is_correct,
			       MIN(s.submitted_at) FILTER (WHERE s.is_correct)
			           OVER (PARTITION BY s.registration_id, s.question_id) AS solved_at
			FROM entrant e
			JOIN submissions s ON s.registration_id = e.id AND s.submitted_at < $2
		), tried AS (
			SELECT registration_id, question_id, MIN(solved_at) AS solved_at,
			       COUNT(*) FILTER (WHERE NOT is_correct AND (solved_at IS NULL OR submitted_at < solved_at)) AS wrong
			FROM answer
			GROUP BY registration_id, question_id
		), first_solve AS (
			SELECT t.question_id, MIN(t.solved_at) AS solved_at
			FROM tried t
			JOIN entrant e ON e.id = t.registration_id
			WHERE NOT e.disqualified
			GROUP BY t.question_id
		), waiting AS (
			SELECT s.registration_id, s.question_id, COUNT(*) AS pending
			FROM entrant e
			JOIN submissions s ON s.registration_id = e.id
			                  AND s.submitted_at >= $5::timestamptz AND s.submitted_at < $6::timestamptz
			WHERE $5::timestamptz IS NOT NULL
			GROUP BY s.registration_id, s.question_id
		), cell AS (
			SELECT e.id AS registration_id, v.pos, t.solved_at,
			       CASE WHEN t.solved_at IS NULL THEN 0
			            ELSE GREATEST(0, COALESCE(floor(extract(epoch FROM t.solved_at - e.start_at) / 60), 0))
			       END::int                                                     AS minute,
			       COALESCE(t.wrong, 0)::int                                    AS wrong,
			       CASE WHEN t.solved_at IS NULL THEN COALESCE(w.pending, 0) ELSE 0 END::int AS pending
			FROM entrant e
			CROSS JOIN visible v
			LEFT JOIN tried t   ON t.registration_id = e.id AND t.question_id = v.id
			LEFT JOIN waiting w ON w.registration_id = e.id AND w.question_id = v.id
		), ranked AS (
			SELECT e.id, e.login, e.full_name, e.account_deleted, e.disqualified,
			       COUNT(c.solved_at)::int                                                   AS solved,
			       COALESCE(SUM(c.minute + k.icpc_penalty_min * c.wrong)
			                    FILTER (WHERE c.solved_at IS NOT NULL), 0)::int               AS penalty,
			       MAX(c.solved_at)                                                          AS last_solved_at,
			       COALESCE(array_agg(c.solved_at ORDER BY c.pos) FILTER (WHERE c.pos IS NOT NULL), '{}') AS solved_ats,
			       COALESCE(array_agg(c.minute ORDER BY c.pos) FILTER (WHERE c.pos IS NOT NULL), '{}')    AS minutes,
			       COALESCE(array_agg(c.wrong ORDER BY c.pos) FILTER (WHERE c.pos IS NOT NULL), '{}')     AS wrongs,
			       COALESCE(array_agg(c.pending ORDER BY c.pos) FILTER (WHERE c.pos IS NOT NULL), '{}')   AS pendings
			FROM entrant e
			CROSS JOIN contest k
			LEFT JOIN cell c ON c.registration_id = e.id
			GROUP BY e.id, e.login, e.full_name, e.account_deleted, e.disqualified
		), page AS (
			SELECT ranked.*,
			       row_number() OVER (ORDER BY solved DESC, penalty ASC, last_solved_at ASC NULLS LAST, id) AS n
			FROM ranked
			ORDER BY n
			LIMIT $4
		), grid AS (
			SELECT (SELECT COUNT(*) FROM visible)::int AS width,
			       (SELECT COALESCE(array_agg(f.solved_at ORDER BY v.pos), '{}')
			        FROM visible v LEFT JOIN first_solve f ON f.question_id = v.id) AS first_solves
		)
		SELECT g.width, CASE WHEN p.n IS NULL OR p.n = 1 THEN g.first_solves END,
		       p.id, p.login, p.full_name, p.account_deleted, p.disqualified, p.solved, p.penalty, p.last_solved_at,
		       p.solved_ats, p.minutes, p.wrongs, p.pendings
		FROM grid g
		LEFT JOIN page p ON true
		ORDER BY p.n`,
		q.ContestID, q.Cutoff, q.IncludeDisqualified, q.Limit, pendingFrom, pendingUntil)
	if err != nil {
		return nil, leaderboard.Grid{}, fmt.Errorf("read icpc standings: %w", err)
	}
	defer rows.Close()

	var (
		grid    leaderboard.Grid
		entries []leaderboard.Entry
		first   = true
	)
	for rows.Next() {
		var (
			width                    int
			firstSolves, solvedAts   []*time.Time
			id                       *uuid.UUID
			login, fullName          *string
			deleted, disqualified    *bool
			solved, penalty          *int
			lastSolvedAt             *time.Time
			minutes, wrongs, pending []int
		)
		if err := rows.Scan(&width, &firstSolves, &id, &login, &fullName, &deleted, &disqualified,
			&solved, &penalty, &lastSolvedAt, &solvedAts, &minutes, &wrongs, &pending); err != nil {
			return nil, leaderboard.Grid{}, fmt.Errorf("scan icpc standings: %w", err)
		}
		if first {
			grid = leaderboard.Grid{Questions: width, FirstSolves: firstSolves}
			first = false
		}
		if id == nil {
			// The grid's own row on a table with nobody on it.
			continue
		}
		e := leaderboard.Entry{
			Registration: *id, Login: *login, FullName: *fullName, AccountDeleted: *deleted, Disqualified: *disqualified,
			Solved: *solved, Penalty: *penalty, LastSolvedAt: lastSolvedAt,
			Cells: make([]leaderboard.Cell, len(solvedAts)),
		}
		for i := range solvedAts {
			e.Cells[i] = leaderboard.Cell{SolvedAt: solvedAts[i], Minute: minutes[i], Wrong: wrongs[i], Pending: pending[i]}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, leaderboard.Grid{}, fmt.Errorf("read icpc standings: %w", err)
	}
	return entries, grid, nil
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
