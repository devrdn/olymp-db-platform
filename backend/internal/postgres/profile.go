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
// The account's registrations are the outer rows, and each counter is a
// lateral aggregate over that one registration's own index range, the way
// the organiser's roster counts a contest's. Solved counts distinct
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
		CROSS JOIN LATERAL (
		    SELECT count(*) AS queries FROM query_log q WHERE q.registration_id = r.id
		) j
		CROSS JOIN LATERAL (
		    SELECT count(DISTINCT s.question_id) AS solved
		    FROM submissions s WHERE s.registration_id = r.id AND s.is_correct
		) a
		WHERE r.user_id = $1`, userID).
		Scan(&s.Contests, &s.Finished, &s.Queries, &s.Solved)
	if err != nil {
		return profile.Summary{}, fmt.Errorf("count the profile of %s: %w", userID, err)
	}
	return s, nil
}

// Enrolments reads the account's registrations with their contests, newest
// first, at most limit of them.
//
// One statement over every registration of the account, never one per
// contest (design §4): the contest arrives whole — its languages and its
// translations included, by the same projection every other contest read
// uses — so the list can be shown in the caller's own language without a
// second round trip per row.
//
// Newest first by when the contest was meant to happen, falling back to when
// it was created for a contest with no schedule yet, and then by the
// registration, so the order is total and two reads agree.
func (r *Profile) Enrolments(ctx context.Context, userID uuid.UUID, limit int) ([]profile.Enrolment, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+contestColumns+`, `+participantColumns+`
		FROM registrations r
		JOIN contests c ON c.id = r.contest_id
		JOIN users u ON u.id = r.user_id
		WHERE r.user_id = $1
		ORDER BY COALESCE(c.starts_at, c.created_at) DESC, r.created_at DESC, r.id
		LIMIT $2`, userID, limit)
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
		if err := row.Scan(targets...); err != nil {
			return profile.Enrolment{}, fmt.Errorf("scan an enrolment: %w", err)
		}
		contest, err := hydrate(e.Contest, settings, languages, translations)
		if err != nil {
			return profile.Enrolment{}, err
		}
		e.Contest = contest
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
