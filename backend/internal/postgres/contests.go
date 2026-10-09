package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Contests implements contests.Repository.
var _ contests.Repository = (*Contests)(nil)

// contestColumns is the projection every contest read shares; its order must
// match contestScanTargets. Languages and translations arrive as JSON from
// correlated subqueries, which avoids an N+1 and reads them in the same
// snapshot as the contest.
const contestColumns = `
	c.id, c.status, c.enrollment, c.question_mode, c.progression, c.scoring, c.timing, c.duration_min,
	c.starts_at, c.ends_at, c.allowed_cidrs, c.settings, c.created_by,
	c.created_at, c.updated_at,
	c.leaderboard_freeze_min, c.leaderboard_names, c.leaderboard_revealed_at,
	c.icpc_penalty_min,
	COALESCE((
		SELECT json_agg(json_build_object('code', cl.lang, 'is_default', cl.is_default)
		                ORDER BY cl.is_default DESC, cl.lang)
		FROM contest_languages cl WHERE cl.contest_id = c.id
	), '[]'::json),
	COALESCE((
		SELECT json_agg(json_build_object('lang', ct.lang, 'title', ct.title,
		                                  'description', COALESCE(ct.description, ''))
		                ORDER BY ct.lang)
		FROM contest_translations ct WHERE ct.contest_id = c.id
	), '[]'::json),
	COALESCE((SELECT cc.hash FROM contest_covers cc WHERE cc.contest_id = c.id), ''),
	COALESCE((SELECT cc.attribution FROM contest_covers cc WHERE cc.contest_id = c.id), '')`

// Contests stores contests in PostgreSQL.
type Contests struct {
	pool *pgxpool.Pool
}

// NewContests returns the contest repository.
func NewContests(pool *pgxpool.Pool) *Contests {
	return &Contests{pool: pool}
}

func (r *Contests) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// languageRow and translationRow are the JSON shapes contestColumns produces.
type languageRow struct {
	Code      string `json:"code"`
	IsDefault bool   `json:"is_default"`
}

type translationRow struct {
	Lang        string `json:"lang"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// The cover comes from correlated subqueries rather than a join, so the
// unique constraint on contest_covers does not decide the listing's row
// count. An empty hash means no uploaded picture: the contest shows the drawn
// cover.

// contestScanTargets returns pointers in contestColumns' order, shared by
// every query that selects it.
func contestScanTargets(c *contests.Contest, settings, languages, translations *[]byte) []any {
	return []any{
		&c.ID, &c.Status, &c.Enrollment, &c.QuestionMode, &c.Progression, &c.Scoring, &c.Timing, &c.DurationMin,
		&c.StartsAt, &c.EndsAt, &c.AllowedCIDRs, settings, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt,
		&c.LeaderboardFreezeMin, &c.LeaderboardNames, &c.LeaderboardRevealedAt,
		&c.ICPCPenaltyMin,
		languages, translations,
		&c.CoverHash, &c.CoverAttribution,
	}
}

func scanContest(row pgx.Row) (contests.Contest, error) {
	var (
		c                                 contests.Contest
		settings, languages, translations []byte
	)
	err := row.Scan(contestScanTargets(&c, &settings, &languages, &translations)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Contest{}, contests.ErrNotFound
	}
	if err != nil {
		return contests.Contest{}, fmt.Errorf("scan contest: %w", err)
	}
	return hydrate(c, settings, languages, translations)
}

func hydrate(c contests.Contest, settings, languages, translations []byte) (contests.Contest, error) {
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &c.Settings); err != nil {
			return contests.Contest{}, fmt.Errorf("decode contest settings: %w", err)
		}
	}

	var langs []languageRow
	if err := json.Unmarshal(languages, &langs); err != nil {
		return contests.Contest{}, fmt.Errorf("decode contest languages: %w", err)
	}
	c.Languages = make([]contests.ContestLanguage, 0, len(langs))
	for _, l := range langs {
		c.Languages = append(c.Languages, contests.ContestLanguage{Code: l.Code, IsDefault: l.IsDefault})
	}

	var texts []translationRow
	if err := json.Unmarshal(translations, &texts); err != nil {
		return contests.Contest{}, fmt.Errorf("decode contest translations: %w", err)
	}
	c.Translations = make(map[string]contests.Translation, len(texts))
	for _, t := range texts {
		c.Translations[t.Lang] = contests.Translation{
			Lang: t.Lang, Title: t.Title, Description: t.Description,
		}
	}
	return c, nil
}

// Create stores a new contest.
func (r *Contests) Create(ctx context.Context, c contests.Contest) (contests.Contest, error) {
	settings, err := json.Marshal(c.Settings)
	if err != nil {
		return contests.Contest{}, fmt.Errorf("encode contest settings: %w", err)
	}

	row := r.querier(ctx).QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO contests (status, enrollment, question_mode, progression, scoring, timing, duration_min,
			                      starts_at, ends_at, allowed_cidrs, settings, created_by,
			                      leaderboard_freeze_min, leaderboard_names, icpc_penalty_min)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			RETURNING *
		)
		SELECT `+contestColumns+` FROM inserted c`,
		c.Status, c.Enrollment, c.QuestionMode, c.Progression, c.Scoring, c.Timing, c.DurationMin,
		c.StartsAt, c.EndsAt, cidrList(c.AllowedCIDRs), settings, c.CreatedBy,
		c.LeaderboardFreezeMin, c.LeaderboardNames, c.ICPCPenaltyMin)

	return scanContest(row)
}

// ByID returns one contest with its languages and translations.
func (r *Contests) ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	return scanContest(r.querier(ctx).QueryRow(ctx,
		`SELECT `+contestColumns+` FROM contests c WHERE c.id = $1`, id))
}

// contestListWhere is Contests.List's filter, with contestListArgs as $1 to
// $5. The page and the count past the end share it.
const contestListWhere = `
		WHERE ($1 = '' OR EXISTS (
		          SELECT 1 FROM contest_translations t
		          WHERE t.contest_id = c.id AND t.title ILIKE '%' || $1 || '%'))
		  AND ($2 = '' OR c.status = $2)
		  AND ($3::uuid IS NULL OR EXISTS (
		          SELECT 1 FROM contest_managers m
		          WHERE m.contest_id = c.id AND m.user_id = $3))
		  AND ($4::uuid IS NULL OR (
		          -- What a participant may see: contests they are on, once
		          -- those are no longer drafts, plus open ones still taking
		          -- signups. A draft is nobody's business but its authors'.
		          (c.status IN ('published', 'running', 'finished')
		           AND EXISTS (SELECT 1 FROM registrations reg
		                       WHERE reg.contest_id = c.id AND reg.user_id = $4))
		          OR (c.enrollment = 'open' AND c.status IN ('published', 'running'))))
		  -- Narrows the visible set above into its two halves: what the person
		  -- is on, and the rest of what is offered to them. It is a second
		  -- AND rather than part of the clause above, which is what stops it
		  -- widening anything: whatever this says, the visibility rule has
		  -- already decided the row may be seen.
		  AND ($5::boolean IS NULL OR $5 = EXISTS (
		          SELECT 1 FROM registrations reg
		          WHERE reg.contest_id = c.id AND reg.user_id = $4))`

func contestListArgs(f contests.Filter) []any {
	// The search text lands in an ILIKE pattern (CLAUDE.md rule 3).
	return []any{escapeLike(f.Query), f.Status, nilUUID(f.ManagedBy), nilUUID(f.VisibleTo), f.Enrolled}
}

// List returns a page of contests and the total matching the filter.
// ManagedBy limits it to an organizer's contests; VisibleTo to what a student
// may see.
//
// The total rides on the page's rows (COUNT(*) OVER()); only a page past the
// end, which has no rows, counts separately, so it still reports the total.
func (r *Contests) List(ctx context.Context, f contests.Filter) ([]contests.Contest, int, error) {
	args := contestListArgs(f)
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+contestColumns+`, COUNT(*) OVER() AS total
		FROM contests c`+contestListWhere+`
		ORDER BY c.starts_at DESC NULLS LAST, c.created_at DESC
		LIMIT $6 OFFSET $7`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list contests: %w", err)
	}
	defer rows.Close()

	var (
		found []contests.Contest
		total int
	)
	for rows.Next() {
		var (
			c            contests.Contest
			settings     []byte
			languages    []byte
			translations []byte
		)
		if err := rows.Scan(append(contestScanTargets(&c, &settings, &languages, &translations), &total)...); err != nil {
			return nil, 0, fmt.Errorf("scan contest: %w", err)
		}
		hydrated, err := hydrate(c, settings, languages, translations)
		if err != nil {
			return nil, 0, err
		}
		found = append(found, hydrated)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list contests: %w", err)
	}
	if len(found) == 0 && f.Offset > 0 {
		if err := r.querier(ctx).QueryRow(ctx,
			`SELECT COUNT(*) FROM contests c`+contestListWhere, args...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count contests: %w", err)
		}
	}
	return found, total, nil
}

// Update saves a contest's own fields. Status is not among them: it moves
// only through SetStatus, so the service's lifecycle rules and publish gate
// cannot be bypassed.
func (r *Contests) Update(ctx context.Context, c contests.Contest) error {
	settings, err := json.Marshal(c.Settings)
	if err != nil {
		return fmt.Errorf("encode contest settings: %w", err)
	}

	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE contests
		SET enrollment = $2, question_mode = $3, progression = $4, scoring = $5, timing = $6, duration_min = $7,
		    starts_at = $8, ends_at = $9, allowed_cidrs = $10, settings = $11,
		    leaderboard_freeze_min = $12, leaderboard_names = $13, icpc_penalty_min = $14,
		    updated_at = now()
		WHERE id = $1`,
		c.ID, c.Enrollment, c.QuestionMode, c.Progression, c.Scoring, c.Timing, c.DurationMin,
		c.StartsAt, c.EndsAt, cidrList(c.AllowedCIDRs), settings,
		c.LeaderboardFreezeMin, c.LeaderboardNames, c.ICPCPenaltyMin)
	if err != nil {
		return fmt.Errorf("update contest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// SetStatus moves the contest to status to, only if it is still in status
// from. The comparison is in the WHERE clause: PostgreSQL locks the row and
// re-evaluates it against the committed value, so of two concurrent callers
// with the same stale status only one matches.
func (r *Contests) SetStatus(ctx context.Context, id uuid.UUID, from, to string) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`UPDATE contests SET status = $3, updated_at = now() WHERE id = $1 AND status = $2`,
		id, from, to)
	if err != nil {
		return fmt.Errorf("set contest status: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// Nothing matched: a second read tells a missing contest from a moved
	// status.
	if _, err := r.ByID(ctx, id); err != nil {
		return err
	}
	return contests.ErrStatusChanged
}

var _ contests.ScheduleRepository = (*Contests)(nil)

// scheduleLockKey is the advisory lock every replica's scheduler tick
// competes for. Advisory locks are scoped to a database, so it cannot collide
// with gamedb's prepareLock.
const scheduleLockKey = 8_531_204_477_119_003_2

// TryLock attempts the scheduler's advisory lock for the ambient transaction.
// It never blocks, so a losing replica returns at once, and the lock is
// released when the transaction ends, by commit or rollback.
func (r *Contests) TryLock(ctx context.Context) (bool, error) {
	var acquired bool
	if err := r.querier(ctx).QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock($1)`, int64(scheduleLockKey),
	).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquire the schedule lock: %w", err)
	}
	return acquired, nil
}

// DueToStart returns every published contest whose starts_at has arrived by
// the database's clock, so replicas with skewed wall clocks agree. It returns
// full rows because the caller re-checks CheckPublishable against each one
// before starting it, so the scheduler cannot start a contest that Transition
// would refuse.
func (r *Contests) DueToStart(ctx context.Context) ([]contests.Contest, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+contestColumns+`
		 FROM contests c
		 WHERE c.status = $1 AND c.starts_at IS NOT NULL AND c.starts_at <= now()`,
		contests.StatusPublished)
	if err != nil {
		return nil, fmt.Errorf("find contests due to start: %w", err)
	}
	defer rows.Close()

	var due []contests.Contest
	for rows.Next() {
		c, err := scanContest(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, c)
	}
	return due, rows.Err()
}

// AdvanceFinished moves to finished every running contest past ends_at plus
// grace. The grace is the same one submissions and contests.Gate allow, so
// the scheduler never closes a contest while a late answer would still be
// accepted.
//
// A contest with no ends_at (individual timing) stays running until an
// organizer moves it.
func (r *Contests) AdvanceFinished(ctx context.Context, grace time.Duration) ([]uuid.UUID, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`UPDATE contests SET status = $1, updated_at = now()
		 WHERE status = $2 AND ends_at IS NOT NULL AND ends_at + $3::interval <= now()
		 RETURNING id`,
		contests.StatusFinished, contests.StatusRunning, grace)
	if err != nil {
		return nil, fmt.Errorf("advance contests to finished: %w", err)
	}
	return scanIDs(rows)
}

// scanIDs collects a single uuid column and closes rows.
func scanIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Delete removes a contest; the schema's cascades remove its dependent rows.
func (r *Contests) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.querier(ctx).Exec(ctx, `DELETE FROM contests WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete contest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// ReplaceLanguages sets the contest's languages to exactly these.
func (r *Contests) ReplaceLanguages(ctx context.Context, id uuid.UUID, langs []contests.ContestLanguage) error {
	codes := make([]string, 0, len(langs))
	defaults := make([]bool, 0, len(langs))
	for _, l := range langs {
		codes = append(codes, l.Code)
		defaults = append(defaults, l.IsDefault)
	}

	// A language left behind would keep the publish gate demanding
	// translations for it.
	const stale = `DELETE FROM contest_languages WHERE contest_id = $1 AND NOT (lang = ANY($2))`
	if len(codes) == 0 {
		exists, err := clearChildren(ctx, r.querier(ctx), stale, "contests", id, codes)
		if err != nil {
			return fmt.Errorf("replace contest languages: %w", err)
		}
		if !exists {
			return contests.ErrNotFound
		}
		return nil
	}
	if _, err := r.querier(ctx).Exec(ctx, stale, id, codes); err != nil {
		return fmt.Errorf("replace contest languages: %w", err)
	}

	// Clear the old default first: the one-default-per-contest unique index is
	// checked row by row and not deferrable, so moving the default from en to
	// ro would collide with the en row not yet rewritten.
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE contest_languages SET is_default = false WHERE contest_id = $1 AND is_default`,
		id); err != nil {
		return fmt.Errorf("replace contest languages: %w", err)
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_languages (contest_id, lang, is_default)
		SELECT $1, code, is_default
		FROM unnest($2::text[], $3::boolean[]) AS t(code, is_default)
		ON CONFLICT (contest_id, lang) DO UPDATE SET is_default = EXCLUDED.is_default`,
		id, codes, defaults)
	if err != nil {
		// A contest deleted meanwhile trips the foreign key.
		return fmt.Errorf("replace contest languages: %w", missingParent(err, map[string]error{
			"contest_languages_contest_id_fkey": contests.ErrNotFound,
		}))
	}
	return nil
}

// ReplaceTranslations sets the contest's authored titles to exactly these.
func (r *Contests) ReplaceTranslations(ctx context.Context, id uuid.UUID, translations []contests.Translation) error {
	langs := make([]string, 0, len(translations))
	titles := make([]string, 0, len(translations))
	descriptions := make([]string, 0, len(translations))
	for _, t := range translations {
		langs = append(langs, t.Lang)
		titles = append(titles, t.Title)
		descriptions = append(descriptions, t.Description)
	}

	const stale = `DELETE FROM contest_translations WHERE contest_id = $1 AND NOT (lang = ANY($2))`
	if len(langs) == 0 {
		exists, err := clearChildren(ctx, r.querier(ctx), stale, "contests", id, langs)
		if err != nil {
			return fmt.Errorf("replace contest translations: %w", err)
		}
		if !exists {
			return contests.ErrNotFound
		}
		return nil
	}
	if _, err := r.querier(ctx).Exec(ctx, stale, id, langs); err != nil {
		return fmt.Errorf("replace contest translations: %w", err)
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_translations (contest_id, lang, title, description)
		SELECT $1, lang, title, NULLIF(description, '')
		FROM unnest($2::text[], $3::text[], $4::text[]) AS t(lang, title, description)
		ON CONFLICT (contest_id, lang) DO UPDATE
		SET title = EXCLUDED.title, description = EXCLUDED.description, updated_at = now()`,
		id, langs, titles, descriptions)
	if err != nil {
		return fmt.Errorf("replace contest translations: %w", missingParent(err, map[string]error{
			"contest_translations_contest_id_fkey": contests.ErrNotFound,
		}))
	}
	return nil
}

// LockContest locks the contest row until the transaction ends, so appointing
// a manager and registering a participant for the same contest cannot both
// succeed for one account. It must run inside a transaction.
//
// FOR NO KEY UPDATE, not FOR UPDATE: every insert referencing the contest by
// foreign key (game_instances from the pool's top-ups, among others) takes a
// key-share lock that FOR UPDATE would block. FOR NO KEY UPDATE still
// conflicts with itself, which is all GrantManager, Enroll and
// AddParticipants need.
//
// A missing contest is ErrNotFound; once found, the row cannot be deleted
// before the transaction ends.
func (r *Contests) LockContest(ctx context.Context, id uuid.UUID) error {
	if !storage.InTx(ctx) {
		return errors.New("locking a contest must run inside a transaction")
	}
	tag, err := r.querier(ctx).Exec(ctx, `SELECT id FROM contests WHERE id = $1 FOR NO KEY UPDATE`, id)
	if err != nil {
		return fmt.Errorf("lock contest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// cidrList stores no restriction as an empty array, not NULL: the column is
// NOT NULL.
func cidrList(prefixes []netip.Prefix) []netip.Prefix {
	if prefixes == nil {
		return []netip.Prefix{}
	}
	return prefixes
}

// nilUUID turns the zero identifier into a NULL parameter, so an unset filter
// disables its clause instead of matching nothing.
func nilUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
