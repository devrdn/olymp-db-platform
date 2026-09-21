package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/profile"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// Profile implements profile.Store: what a participant is shown of their own
// account.
//
// Every statement here starts from the account's own registrations
// (registrations_user_id_idx) or from one registration of theirs, and reads
// the journals only as a range of an index on registration_id — query_log
// (registration_id, executed_at, id) and submissions_registration_submitted_idx,
// both from migration 33. TestProfileReadsScanNoJournal proves the plans on a
// database holding a representative year: a profile is a page anybody signed
// in may open, so it must not be a way to read the largest tables in the
// installation end to end.
//
// It computes no result. Points, solved, penalty and place are
// leaderboard.Service.Own's, and the report's questions are the answers tab
// internal/monitor already reads; what is left for storage is the list
// itself and two counts of a registration's own journal.
var _ profile.Store = (*Profile)(nil)

// profileStatuses are the contest statuses a profile carries: published,
// running, finished and archived.
//
// A draft is excluded, for the reason Contests.List excludes it from the
// participant catalogue — a draft is nobody's business but its authors'. A
// roster may be filled while a contest is still being written, so a
// registration in one exists long before anybody is meant to know the contest
// does; without this clause a member of that roster would read its title, its
// status and its schedule here.
//
// Archived is kept deliberately. A profile is a history view, and archiving is
// how a finished olympiad is put away rather than how it is taken from the
// people who sat it.
//
// Spelled into both statements below, so the four numbers of the header count
// exactly the rows the list shows.
const profileStatuses = `c.status IN ('published', 'running', 'finished', 'archived')`

// Profile reads a participant's own account.
type Profile struct {
	pool *pgxpool.Pool
	// wrap, when set, wraps every querier these reads use. Only this
	// package's tests set it, to EXPLAIN exactly the statements sent.
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

// Summary counts the profile's four numbers in one statement.
//
// The account's registrations are the outer rows — the ones profileStatuses
// admits, so these numbers describe exactly the contests Enrolments lists —
// and each counter is a lateral aggregate over that one registration's own
// index range, the way the organiser's roster counts a contest's. Solved
// counts distinct
// questions, so two correct attempts at one question are one solved
// question, and a question answered correctly in two contests counts in
// both — this is what the account did, not how many questions exist.
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
		WHERE r.user_id = $1 AND `+profileStatuses, userID).
		Scan(&s.Contests, &s.Finished, &s.Queries, &s.Solved)
	if err != nil {
		return profile.Summary{}, fmt.Errorf("count the profile of %s: %w", userID, err)
	}
	return s, nil
}

// ownResultColumns is the participant's own result in one contest, counted
// from their own submissions and nothing else.
//
// Deliberately the same two expressions postgres.Leaderboard.Standings uses
// for points and solved, and the same cell arithmetic ICPCStandings uses for
// the ICPC pair — the list cannot ask the leaderboard for them (that is a
// whole table per contest, which is the one thing this statement exists to
// avoid), so what keeps the two answers one answer is a set of tests that run
// both against the same data and compare:
// TestProfileEnrolmentsCarryTheOwnResultTheLeaderboardAgreesWith, its ICPC
// twin, and TestProfileEnrolmentsCarryTheICPCResultUnderAnIndividualTimer,
// which pins the one branch of the penalty arithmetic a fixed-timing fixture
// would never reach. A change to either formula fails them.
//
// Cut off at nothing. A freeze hides other people's progress; it never hides
// a participant's own work from them once their contest has ended, which is
// the same choice leaderboard.Service.Own makes for the report.
//
// Points are zero in ICPC scoring, as they are on the table: Service.Submit
// writes points_awarded = 0 in that mode, but a contest switched out of it
// before it ran can have rows from the mode before, and summing those would
// have the list report a score the report and the standings both call zero.
//
// Both aggregates are one range of submissions_registration_submitted_idx
// per registration. The ICPC one is joined on the contest's visible
// questions, because a question off the grid costs and counts nothing, and
// it is evaluated only for a contest actually scored that way.
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

// Enrolments reads the account's registrations with their contests and the
// participant's own result in each, newest first, at most limit of them.
//
// Only the statuses profileStatuses names: a draft the account is already on
// the roster of is left out, as the participant catalogue leaves it out.
//
// One statement over every registration of the account, never one per
// contest (design §4): the contest arrives whole — its languages and its
// translations included, by the same projection every other contest read
// uses — so the list can be shown in the caller's own language without a
// second round trip per row, and the row's own numbers come with it
// (ownResultColumns) rather than from a table computed per contest.
//
// The page is chosen before anything is counted. The result columns are an
// aggregate per row, and an account may be registered in far more contests
// than one profile shows: ordering and cutting first means fifty aggregates
// for fifty rows rather than one per registration the account ever had.
//
// Newest first by when the contest was meant to happen, falling back to when
// it was created for a contest with no schedule yet, and then by the
// registration, so the order is total and two reads agree. Stated twice —
// once to pick the page, once to return it in order — because a join over
// the page does not preserve its order.
func (r *Profile) Enrolments(ctx context.Context, userID uuid.UUID, limit int) ([]profile.Enrolment, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH page AS (
		    SELECT r.id
		    FROM registrations r
		    JOIN contests c ON c.id = r.contest_id
		    WHERE r.user_id = $1 AND `+profileStatuses+`
		    ORDER BY COALESCE(c.starts_at, c.created_at) DESC, r.created_at DESC, r.id
		    LIMIT $2
		)
		SELECT `+contestColumns+`, `+participantColumns+`, `+ownResultColumns+`
		FROM page
		JOIN registrations r ON r.id = page.id
		JOIN contests c ON c.id = r.contest_id
		JOIN users u ON u.id = r.user_id`+ownResultJoins+`
		ORDER BY COALESCE(c.starts_at, c.created_at) DESC, r.created_at DESC, r.id`,
		userID, limit)
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
		// The mode travels with the numbers, so nothing downstream has to
		// look the contest up again to know which of them is the result.
		e.Result.Scoring = contest.Scoring
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the contests of %s: %w", userID, err)
	}
	return enrolments, nil
}

// Activity counts one registration's queries, how many of them succeeded,
// and when it last answered.
//
// One read of the query log's range for the registration rather than two:
// the successful ones are a filtered count inside the same aggregate, not a
// second scan of the same range for a second number.
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
