package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Clarifications stores participants' questions, their answers and the
// organisers' announcements. Every read is a range of an index migration
// 000049 made for it.
type Clarifications struct {
	pool *pgxpool.Pool
	uow  *storage.PgxUnitOfWork
	// wrap, when set, wraps every querier, so a test can EXPLAIN what runs.
	wrap func(storage.Querier) storage.Querier
}

var _ clarifications.Repository = (*Clarifications)(nil)

func NewClarifications(pool *pgxpool.Pool) *Clarifications {
	return &Clarifications{pool: pool, uow: storage.NewUnitOfWork(pool)}
}

func (r *Clarifications) querier(ctx context.Context) storage.Querier {
	q := storage.QuerierFrom(ctx, r.pool)
	if r.wrap != nil {
		return r.wrap(q)
	}
	return q
}

// clarificationsLockClass is the first key of the per-registration advisory
// lock Ask counts under. An advisory lock rather than the registration's row
// keeps that row, which the clock and scoring update, free.
const clarificationsLockClass = 0x434c5246

const clarificationColumns = `id, contest_id, registration_id, question_id, body, created_at,
	answer, declined, answered_by, answered_at, edited_at, visibility`

// clarificationRow is one row of clarificationColumns as scanned.
type clarificationRow struct {
	c          clarifications.Clarification
	answer     *string
	declined   bool
	by         *uuid.UUID
	answeredAt *time.Time
	editedAt   *time.Time
	visibility string
}

func (r *clarificationRow) dest() []any {
	return []any{&r.c.ID, &r.c.ContestID, &r.c.RegistrationID, &r.c.QuestionID, &r.c.Body, &r.c.CreatedAt,
		&r.answer, &r.declined, &r.by, &r.answeredAt, &r.editedAt, &r.visibility}
}

func (r clarificationRow) clarification() clarifications.Clarification {
	c := r.c
	if r.answeredAt != nil && r.answer != nil {
		c.Answer = &clarifications.Answer{
			Body: *r.answer, Declined: r.declined, Visibility: clarifications.Visibility(r.visibility),
			By: r.by, At: *r.answeredAt, EditedAt: r.editedAt,
		}
	}
	return c
}

// scanClarification scans clarificationColumns, then extra.
func scanClarification(row pgx.Row, extra ...any) (clarifications.Clarification, error) {
	var r clarificationRow
	if err := row.Scan(append(r.dest(), extra...)...); err != nil {
		return clarifications.Clarification{}, err
	}
	return r.clarification(), nil
}

// Ask counts the registration's open questions and inserts under one advisory
// lock, so two asks at the last open place cannot both pass. Whether the asker
// can reach the question is decided by the insert itself, in its own snapshot,
// rather than left to the foreign key, whose violation would abort a caller's
// transaction; the key still catches a question deleted meanwhile.
func (r *Clarifications) Ask(ctx context.Context, c clarifications.Clarification, openLimit int, sequential bool) (clarifications.Clarification, error) {
	var stored clarifications.Clarification
	err := r.uow.Do(ctx, func(ctx context.Context) error {
		q := r.querier(ctx)
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`,
			clarificationsLockClass, c.RegistrationID.String()); err != nil {
			return fmt.Errorf("lock the registration's questions: %w", err)
		}
		var open int
		if err := q.QueryRow(ctx, `
			SELECT count(*) FROM clarifications
			WHERE registration_id = $1 AND answered_at IS NULL`, c.RegistrationID).Scan(&open); err != nil {
			return fmt.Errorf("count the open questions: %w", err)
		}
		if open >= openLimit {
			return clarifications.ErrTooManyOpen
		}
		var err error
		stored, err = scanClarification(q.QueryRow(ctx, `
			INSERT INTO clarifications (contest_id, registration_id, question_id, body, created_at)
			SELECT $1, $2, $3, $4, $5
			WHERE $3::uuid IS NULL OR EXISTS (
				SELECT 1 FROM questions q
				WHERE q.id = $3 AND `+questionReachable("q", "$1", "$2", "$6::boolean")+`
			)
			RETURNING `+clarificationColumns,
			c.ContestID, c.RegistrationID, c.QuestionID, c.Body, c.CreatedAt, sequential))
		if errors.Is(err, pgx.ErrNoRows) {
			return clarifications.ErrQuestionNotFound
		}
		if err != nil {
			return missingParent(fmt.Errorf("store the question: %w", err), map[string]error{
				"clarifications_question_contest_fkey": clarifications.ErrQuestionNotFound,
			})
		}
		return nil
	})
	if err != nil {
		return clarifications.Clarification{}, err
	}
	return stored, nil
}

// Feed merges three ranges, each cut at limit before the merge: the
// registration's own questions, the contest's public answers (the asker's own
// public ones come with the first), and the announcements. A public answer
// names its question only for a reader who can reach it, which costs a few
// index probes per row of one page.
func (r *Clarifications) Feed(ctx context.Context, contestID, registrationID uuid.UUID, before clarifications.Cursor, limit int, sequential bool) ([]clarifications.Entry, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT kind, `+clarificationColumns+`
		FROM (
			(SELECT 'c' AS kind, `+clarificationColumns+`, posted_at
			 FROM clarifications
			 WHERE registration_id = $2 AND contest_id = $1 AND (posted_at, id) < ($3, $4)
			 ORDER BY posted_at DESC, id DESC
			 LIMIT $5)
			UNION ALL
			(SELECT 'c', c.id, c.contest_id, c.registration_id,
			        CASE WHEN EXISTS (
			            SELECT 1 FROM questions q
			            WHERE q.id = c.question_id AND `+questionReachable("q", "$1", "$2", "$6::boolean")+`
			        ) THEN c.question_id END,
			        c.body, c.created_at, c.answer, c.declined, c.answered_by, c.answered_at, c.edited_at,
			        c.visibility, c.posted_at
			 FROM clarifications c
			 WHERE contest_id = $1 AND visibility = 'public' AND registration_id <> $2
			   AND (posted_at, id) < ($3, $4)
			 ORDER BY posted_at DESC, id DESC
			 LIMIT $5)
			UNION ALL
			(SELECT 'a', id, contest_id, '00000000-0000-0000-0000-000000000000'::uuid, NULL::uuid, body, created_at,
			        NULL::text, false, author, NULL::timestamptz, NULL::timestamptz, 'public', created_at
			 FROM announcements
			 WHERE contest_id = $1 AND (created_at, id) < ($3, $4)
			 ORDER BY created_at DESC, id DESC
			 LIMIT $5)
		) AS feed
		ORDER BY posted_at DESC, id DESC
		LIMIT $5`, contestID, registrationID, before.At, before.ID, limit, sequential)
	if err != nil {
		return nil, fmt.Errorf("read the feed: %w", err)
	}
	defer rows.Close()

	var entries []clarifications.Entry
	for rows.Next() {
		var kind string
		var row clarificationRow
		if err := rows.Scan(append([]any{&kind}, row.dest()...)...); err != nil {
			return nil, fmt.Errorf("read a feed entry: %w", err)
		}
		if kind == "a" {
			// An announcement's author rides in the column of who answered.
			entries = append(entries, clarifications.Entry{Announcement: &clarifications.Announcement{
				ID: row.c.ID, ContestID: row.c.ContestID, Body: row.c.Body, Author: row.by, CreatedAt: row.c.CreatedAt,
			}})
			continue
		}
		c := row.clarification()
		entries = append(entries, clarifications.Entry{Clarification: &c})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the feed: %w", err)
	}
	return entries, nil
}

func (r *Clarifications) OpenCount(ctx context.Context, registrationID uuid.UUID) (int, error) {
	var open int
	if err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*) FROM clarifications
		WHERE registration_id = $1 AND answered_at IS NULL`, registrationID).Scan(&open); err != nil {
		return 0, fmt.Errorf("count the open questions: %w", err)
	}
	return open, nil
}

// queueOrder is each state's filter, the keyset past the clarification $3
// (skipped when $3 is null), and order, matching its partial index. The keyset
// is the cursor row's own sort key, so a row that changed state since still
// marks a place.
var queueOrder = map[clarifications.State]string{
	clarifications.StateOpen: `c.answered_at IS NULL
		AND ($3::uuid IS NULL OR (c.created_at, c.id) >
		     (SELECT created_at, id FROM clarifications WHERE id = $3 AND contest_id = $1))
		ORDER BY c.created_at, c.id`,
	clarifications.StateAnswered: `c.answered_at IS NOT NULL
		AND ($3::uuid IS NULL OR (c.answered_at, c.id) <
		     (SELECT answered_at, id FROM clarifications WHERE id = $3 AND contest_id = $1))
		ORDER BY c.answered_at DESC, c.id DESC`,
}

// askedSelect reads clarifications c with who asked and who answered, for
// scanAsked.
const askedSelect = `
	SELECT c.id, c.contest_id, c.registration_id, c.question_id, c.body, c.created_at,
	       c.answer, c.declined, c.answered_by, c.answered_at, c.edited_at, c.visibility,
	       u.login, u.full_name, coalesce(s.login, '')
	FROM clarifications c
	JOIN registrations r ON r.id = c.registration_id
	JOIN users u ON u.id = r.user_id
	LEFT JOIN users s ON s.id = c.answered_by`

func scanAsked(row pgx.Row) (clarifications.Asked, error) {
	var a clarifications.Asked
	var err error
	a.Clarification, err = scanClarification(row, &a.AskerLogin, &a.AskerFullName, &a.AnsweredByLogin)
	return a, err
}

func (r *Clarifications) Queue(ctx context.Context, contestID uuid.UUID, state clarifications.State, limit int, after uuid.UUID) ([]clarifications.Asked, error) {
	order, ok := queueOrder[state]
	if !ok {
		return nil, fmt.Errorf("unknown queue state %q", state)
	}
	var cursor *uuid.UUID
	if after != uuid.Nil {
		cursor = &after
	}
	rows, err := r.querier(ctx).Query(ctx, askedSelect+`
		WHERE c.contest_id = $1 AND `+order+`
		LIMIT $2`, contestID, limit, cursor)
	if err != nil {
		return nil, fmt.Errorf("read the queue: %w", err)
	}
	defer rows.Close()

	var items []clarifications.Asked
	for rows.Next() {
		a, err := scanAsked(rows)
		if err != nil {
			return nil, fmt.Errorf("read a queued question: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the queue: %w", err)
	}
	return items, nil
}

func (r *Clarifications) Asked(ctx context.Context, contestID, id uuid.UUID) (clarifications.Asked, error) {
	a, err := scanAsked(r.querier(ctx).QueryRow(ctx, askedSelect+`
		WHERE c.id = $1 AND c.contest_id = $2`, id, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return clarifications.Asked{}, clarifications.ErrNotFound
	}
	if err != nil {
		return clarifications.Asked{}, fmt.Errorf("read the clarification: %w", err)
	}
	return a, nil
}

func (r *Clarifications) Counts(ctx context.Context, contestID uuid.UUID) (clarifications.Counts, error) {
	var counts clarifications.Counts
	if err := r.querier(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM clarifications WHERE contest_id = $1 AND answered_at IS NULL),
		       (SELECT count(*) FROM clarifications WHERE contest_id = $1 AND answered_at IS NOT NULL)`,
		contestID).Scan(&counts.Open, &counts.Answered); err != nil {
		return clarifications.Counts{}, fmt.Errorf("count the questions: %w", err)
	}
	return counts, nil
}

// diagnose tells, after a write matched no row, whether the clarification is
// absent from the contest or in the wrong state for the write.
func (r *Clarifications) diagnose(ctx context.Context, contestID, id uuid.UUID, answeredErr, unansweredErr error) error {
	var answered bool
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT answered_at IS NOT NULL FROM clarifications WHERE id = $1 AND contest_id = $2`,
		id, contestID).Scan(&answered)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return clarifications.ErrNotFound
	case err != nil:
		return fmt.Errorf("read the clarification: %w", err)
	case answered:
		return answeredErr
	default:
		return unansweredErr
	}
}

func (r *Clarifications) Answer(ctx context.Context, contestID, id uuid.UUID, answer clarifications.Answer) (clarifications.Clarification, error) {
	answered, err := scanClarification(r.querier(ctx).QueryRow(ctx, `
		UPDATE clarifications
		SET answer = $3, declined = $4, visibility = $5, answered_by = $6, answered_at = $7
		WHERE id = $1 AND contest_id = $2 AND answered_at IS NULL
		RETURNING `+clarificationColumns,
		id, contestID, answer.Body, answer.Declined, string(answer.Visibility), answer.By, answer.At))
	if errors.Is(err, pgx.ErrNoRows) {
		return clarifications.Clarification{}, r.diagnose(ctx, contestID, id, clarifications.ErrAlreadyAnswered,
			fmt.Errorf("the answer to clarification %s matched no row while it waits", id))
	}
	if err != nil {
		return clarifications.Clarification{}, fmt.Errorf("store the answer: %w", err)
	}
	return answered, nil
}

// EditAnswer reads the answer it replaces from the locked row, so racing
// edits each report what they replaced.
func (r *Clarifications) EditAnswer(ctx context.Context, contestID, id uuid.UUID, answer clarifications.Answer) (clarifications.Clarification, clarifications.Clarification, error) {
	var (
		oldAnswer     string
		oldDeclined   bool
		oldVisibility string
		oldEditedAt   *time.Time
	)
	after, err := scanClarification(r.querier(ctx).QueryRow(ctx, `
		UPDATE clarifications AS c
		SET answer = $3, declined = $4, visibility = $5, edited_at = $6
		FROM (
			SELECT id, answer, declined, visibility, edited_at FROM clarifications
			WHERE id = $1 AND contest_id = $2 AND answered_at IS NOT NULL
			FOR UPDATE
		) AS prior
		WHERE c.id = prior.id
		RETURNING c.id, c.contest_id, c.registration_id, c.question_id, c.body, c.created_at,
		          c.answer, c.declined, c.answered_by, c.answered_at, c.edited_at, c.visibility,
		          prior.answer, prior.declined, prior.visibility, prior.edited_at`,
		id, contestID, answer.Body, answer.Declined, string(answer.Visibility), answer.At),
		&oldAnswer, &oldDeclined, &oldVisibility, &oldEditedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return clarifications.Clarification{}, clarifications.Clarification{},
			r.diagnose(ctx, contestID, id,
				fmt.Errorf("the edit of clarification %s matched no row while it is answered", id), clarifications.ErrNotAnswered)
	}
	if err != nil {
		return clarifications.Clarification{}, clarifications.Clarification{}, fmt.Errorf("edit the answer: %w", err)
	}
	before := after
	previous := *after.Answer
	previous.Body, previous.Declined = oldAnswer, oldDeclined
	previous.Visibility, previous.EditedAt = clarifications.Visibility(oldVisibility), oldEditedAt
	before.Answer = &previous
	return before, after, nil
}

const announcementColumns = `id, contest_id, body, author, created_at`

func scanAnnouncement(row pgx.Row) (clarifications.Announcement, error) {
	var a clarifications.Announcement
	err := row.Scan(&a.ID, &a.ContestID, &a.Body, &a.Author, &a.CreatedAt)
	return a, err
}

func (r *Clarifications) Announce(ctx context.Context, a clarifications.Announcement) (clarifications.Announcement, error) {
	posted, err := scanAnnouncement(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO announcements (contest_id, body, author, created_at)
		VALUES ($1, $2, $3, $4)
		RETURNING `+announcementColumns, a.ContestID, a.Body, a.Author, a.CreatedAt))
	if err != nil {
		return clarifications.Announcement{}, fmt.Errorf("store the announcement: %w", err)
	}
	return posted, nil
}

func (r *Clarifications) Announcements(ctx context.Context, contestID uuid.UUID, limit, offset int) ([]clarifications.Announcement, int, error) {
	q := r.querier(ctx)
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM announcements WHERE contest_id = $1`, contestID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the announcements: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT `+announcementColumns+` FROM announcements
		WHERE contest_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3`, contestID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("read the announcements: %w", err)
	}
	defer rows.Close()
	var items []clarifications.Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("read an announcement: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read the announcements: %w", err)
	}
	return items, total, nil
}
