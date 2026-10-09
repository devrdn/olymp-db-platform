package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/profile"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// Every statement here starts from the account's own registrations
// (registrations_user_id_idx) and reads the journals only as a range of an
// index on registration_id (query_log and submissions, migration 33). Any
// signed-in user may open a profile, so it must not scan the largest tables;
// TestProfileReadsScanNoJournal checks the plans.
var _ profile.Store = (*Profile)(nil)

// Profile reads a participant's own account.
type Profile struct {
	pool *pgxpool.Pool
	// wrap, when set, wraps every querier; tests use it to EXPLAIN the
	// statements sent.
	wrap func(storage.Querier) storage.Querier
}

// NewProfile returns the participant's own read side over pool.
func NewProfile(pool *pgxpool.Pool) *Profile { return &Profile{pool: pool} }

func (r *Profile) querier(ctx context.Context) storage.Querier {
	q := storage.QuerierFrom(ctx, r.pool)
	if r.wrap != nil {
		return r.wrap(q)
	}
	return q
}

// Summary counts the profile's four numbers in one statement, over the same
// public contests Enrolments lists.
//
// It has no limit: these numbers are the account's whole record, not one
// page. The work is bounded by the account's own registrations, each counter
// a lateral aggregate over one registration's index range. Solved counts
// distinct questions per registration.
func (r *Profile) Summary(ctx context.Context, userID uuid.UUID) (profile.Summary, error) {
	var s profile.Summary
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE r.status = 'finished')::int,
		       COALESCE(SUM(j.queries), 0)::int,
		       COALESCE(SUM(a.solved), 0)::int
		FROM registrations r
		JOIN contests c ON c.id = r.contest_id
		CROSS JOIN LATERAL (
		    SELECT count(*) AS queries FROM query_log q WHERE q.registration_id = r.id
		) j
		CROSS JOIN LATERAL (
		    SELECT count(DISTINCT s.question_id) AS solved
		    FROM submissions s WHERE s.registration_id = r.id AND s.is_correct
		) a
		WHERE r.user_id = $1 AND `+publicStatusFilter("c.status", 2), userID, contests.PublicStatuses).
		Scan(&s.Contests, &s.Finished, &s.Queries, &s.Solved)
	if err != nil {
		return profile.Summary{}, fmt.Errorf("count the profile of %s: %w", userID, err)
	}
	return s, nil
}

// ownResultColumns is the participant's own result in one contest, from their
// own submissions only.
//
// The expressions repeat Leaderboard.Standings and ICPCStandings, since asking
// the leaderboard would compute a whole table per contest. The
// TestProfileEnrolmentsCarry... tests compare the two on the same data.
//
// There is no freeze cut-off: a freeze never hides a participant's own work.
// Points are forced to zero under ICPC, because a contest switched out of
// another mode may hold earlier points. The ICPC aggregate counts only
// visible questions.
const ownResultColumns = `
	CASE WHEN c.scoring = 'icpc' THEN 0 ELSE COALESCE(points.points, 0) END::int,
	CASE WHEN c.scoring = 'icpc' THEN COALESCE(icpc.solved, 0) ELSE COALESCE(points.solved, 0) END::int,
	COALESCE(icpc.penalty, 0)::int`

// ownResultJoins are the two laterals ownResultColumns reads.
const ownResultJoins = `
	LEFT JOIN LATERAL (
	    SELECT COALESCE(SUM(s.points_awarded), 0)                          AS points,
	           COUNT(DISTINCT s.question_id) FILTER (WHERE s.is_correct)   AS solved
	    FROM submissions s WHERE s.registration_id = r.id
	) points ON true
	LEFT JOIN LATERAL (
	    SELECT COUNT(*) AS solved,
	           COALESCE(SUM(
	               GREATEST(0, COALESCE(floor(extract(epoch FROM t.solved_at - CASE
	                   WHEN c.timing = 'individual' THEN COALESCE(r.started_at, c.starts_at)
	                   ELSE c.starts_at END) / 60), 0))
	               + c.icpc_penalty_min * t.wrong), 0) AS penalty
	    FROM (
	        SELECT a.question_id, MIN(a.solved_at) AS solved_at,
	               COUNT(*) FILTER (WHERE NOT a.is_correct
	                   AND (a.solved_at IS NULL OR a.submitted_at < a.solved_at)) AS wrong
	        FROM (
	            SELECT s.question_id, s.submitted_at, s.is_correct,
	                   MIN(s.submitted_at) FILTER (WHERE s.is_correct)
	                       OVER (PARTITION BY s.question_id) AS solved_at
	            FROM submissions s
	            JOIN questions q ON q.id = s.question_id AND q.contest_id = c.id AND q.is_visible
	            WHERE s.registration_id = r.id
	        ) a
	        GROUP BY a.question_id
	    ) t
	    WHERE t.solved_at IS NOT NULL
	) icpc ON c.scoring = 'icpc'`

// Enrolments reads the account's registrations in public contests, with each
// contest and the participant's own result, newest first, at most limit.
//
// The page is chosen before anything is aggregated, so the result columns
// are computed for limit rows only. The ORDER BY is total and appears twice
// because the join over the page does not preserve its order.
func (r *Profile) Enrolments(ctx context.Context, userID uuid.UUID, limit int) ([]profile.Enrolment, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH page AS (
		    SELECT r.id
		    FROM registrations r
		    JOIN contests c ON c.id = r.contest_id
		    WHERE r.user_id = $1 AND `+publicStatusFilter("c.status", 3)+`
		    ORDER BY COALESCE(c.starts_at, c.created_at) DESC, r.created_at DESC, r.id
		    LIMIT $2
		)
		SELECT `+contestColumns+`, `+participantColumns+`, `+ownResultColumns+`
		FROM page
		JOIN registrations r ON r.id = page.id
		JOIN contests c ON c.id = r.contest_id
		JOIN users u ON u.id = r.user_id`+ownResultJoins+`
		ORDER BY COALESCE(c.starts_at, c.created_at) DESC, r.created_at DESC, r.id`,
		userID, limit, contests.PublicStatuses)
	if err != nil {
		return nil, fmt.Errorf("list the contests of %s: %w", userID, err)
	}
	enrolments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (profile.Enrolment, error) {
		var (
			e                                 profile.Enrolment
			settings, languages, translations []byte
		)
		targets := contestScanTargets(&e.Contest, &settings, &languages, &translations)
		targets = append(targets, participantScanTargets(&e.Participant)...)
		targets = append(targets, &e.Result.Points, &e.Result.Solved, &e.Result.Penalty)
		if err := row.Scan(targets...); err != nil {
			return profile.Enrolment{}, fmt.Errorf("scan an enrolment: %w", err)
		}
		contest, err := hydrate(e.Contest, settings, languages, translations)
		if err != nil {
			return profile.Enrolment{}, err
		}
		e.Contest = contest
		e.Result.Scoring = contest.Scoring
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the contests of %s: %w", userID, err)
	}
	return enrolments, nil
}

// Activity counts one registration's queries, how many succeeded, and when it
// last answered, in one pass over its query log range.
func (r *Profile) Activity(ctx context.Context, registration uuid.UUID) (profile.Activity, error) {
	var a profile.Activity
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT j.queries, j.successful,
		       (SELECT max(s.submitted_at) FROM submissions s WHERE s.registration_id = $1)
		FROM (
		    SELECT count(*)::int AS queries,
		           (count(*) FILTER (WHERE q.status = $2))::int AS successful
		    FROM query_log q WHERE q.registration_id = $1
		) j`, registration, queryrunner.StatusOK).
		Scan(&a.Queries, &a.Successful, &a.LastAnswerAt)
	if err != nil {
		return profile.Activity{}, fmt.Errorf("count the activity of %s: %w", registration, err)
	}
	return a, nil
}
