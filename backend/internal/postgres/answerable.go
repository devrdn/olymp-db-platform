package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Answerable implements queryproxy.Answerable.
var _ queryproxy.Answerable = (*Answerable)(nil)

// Answerable answers whether a registration still has a question of a contest
// it could get an answer out of — the one fact the SQL console needs before
// it takes another query (queryproxy.ErrNothingLeftToAnswer). It reads
// questions and submissions and writes neither.
type Answerable struct {
	pool *pgxpool.Pool
}

// NewAnswerable returns the reader behind the console's own closing rule.
func NewAnswerable(pool *pgxpool.Pool) *Answerable {
	return &Answerable{pool: pool}
}

func (r *Answerable) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// AnswerableLeft reports whether any question of contestID is still open to
// registrationID: not answered correctly, and not out of attempts.
//
// "Closed" here is the same definition the participant's own question list
// already uses — contests.Reader's isClosed, and the identical predicate
// inside Sequence.Open and Sequence.Frontier: a correct submission closes a
// question outright, and otherwise it closes once the submissions already
// committed reach the question's own max_attempts (a NULL cap never closes
// this way). The three must agree, because a list that offers an answer while
// this says there is nothing left would take the console away from a
// participant with work still in front of them; answerable_test.go pins that
// agreement against contests.Reader itself rather than against a second copy
// of its rules.
//
// The one place this is narrower than Sequence's own predicate is is_visible.
// The sequence counts hidden questions because they take part in the
// *ordering* (§6.1.1), but a participant is never given a hidden question's
// identifier, so they can never submit one and it can never close — counting
// it here would hold the console open forever, in every contest that has one,
// on the strength of a question nobody can work towards. It is still wider
// than the participant's own list in the other direction, which is the safe
// one: the list also drops a visible question with no translation in the
// reader's language, and this counts it, so the console can only ever stay
// open longer than the list is empty, never close sooner.
//
// A contest with no visible question at all answers true rather than false,
// which is the second half of that same "wider, never narrower" direction.
// The refusal this feeds means "you have answered everything you were
// given"; a contest that showed the participant nothing never gave them
// anything to finish, and closing their console at minute zero over a
// question they were never shown would be a configuration mistake reported
// as their own progress. It is also what keeps a contest still being
// authored — questions written but not yet made visible — from taking the
// console away from anybody who reaches it.
//
// One statement and one round trip whatever the contest's size: EXISTS
// short-circuits at the first open question, and every filter is served by an
// index that already exists (CLAUDE.md rule 7). questions.contest_id is the
// leading column of the table's own UNIQUE (contest_id, ord); each subquery
// on submissions is filtered by (registration_id, question_id), the leading
// two columns of that table's UNIQUE (registration_id, question_id,
// attempt_no) — the same pair Sequence.Open relies on. Nothing here scales
// with the number of registrations or contests in the installation.
func (r *Answerable) AnswerableLeft(ctx context.Context, contestID, registrationID uuid.UUID) (bool, error) {
	var left bool
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM questions q
			WHERE q.contest_id = $1 AND q.is_visible
			  AND NOT (
			      EXISTS (
			          SELECT 1 FROM submissions s
			          WHERE s.registration_id = $2 AND s.question_id = q.id AND s.is_correct
			      )
			      OR (
			          q.max_attempts IS NOT NULL
			          AND (SELECT COUNT(*) FROM submissions s2
			               WHERE s2.registration_id = $2 AND s2.question_id = q.id) >= q.max_attempts
			      )
			  )
		) OR NOT EXISTS (
			SELECT 1 FROM questions q2 WHERE q2.contest_id = $1 AND q2.is_visible
		)`, contestID, registrationID).Scan(&left)
	if err != nil {
		return false, fmt.Errorf("check what is still answerable in contest %s: %w", contestID, err)
	}
	return left, nil
}
