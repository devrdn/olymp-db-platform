package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Contests implements contests.Repository.
var _ contests.Repository = (*Contests)(nil)

// contestColumns is the projection every contest read shares, so a new column
// is added in one place and the scan order cannot drift between queries.
//
// The languages and translations arrive as JSON from correlated subqueries
// rather than as extra round trips: the publish gate reasons about a contest
// together with them, and loading them separately would be both an N+1 and a
// chance for the two to disagree.
const contestColumns = `
	c.id, c.status, c.enrollment, c.question_mode, c.timing, c.duration_min,
	c.starts_at, c.ends_at, c.allowed_cidrs, c.settings, c.created_by,
	c.created_at, c.updated_at,
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
	), '[]'::json)`

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

// languageRow and translationRow are the JSON shapes the projection above
// produces. They live here rather than on the domain types because the shape
// is a storage detail: the domain does not know it is ever serialised.
type languageRow struct {
	Code      string `json:"code"`
	IsDefault bool   `json:"is_default"`
}

type translationRow struct {
	Lang        string `json:"lang"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func scanContest(row pgx.Row) (contests.Contest, error) {
	var (
		c            contests.Contest
		settings     []byte
		languages    []byte
		translations []byte
	)
	err := row.Scan(
		&c.ID, &c.Status, &c.Enrollment, &c.QuestionMode, &c.Timing, &c.DurationMin,
		&c.StartsAt, &c.EndsAt, &c.AllowedCIDRs, &settings, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt, &languages, &translations,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Contest{}, contests.ErrNotFound
	}
	if err != nil {
		return contests.Contest{}, fmt.Errorf("scan contest: %w", err)
	}
	return hydrate(c, settings, languages, translations)
}

// hydrate turns the JSON columns into the domain's own types.
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
			INSERT INTO contests (status, enrollment, question_mode, timing, duration_min,
			                      starts_at, ends_at, allowed_cidrs, settings, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING *
		)
		SELECT `+contestColumns+` FROM inserted c`,
		c.Status, c.Enrollment, c.QuestionMode, c.Timing, c.DurationMin,
		c.StartsAt, c.EndsAt, cidrList(c.AllowedCIDRs), settings, c.CreatedBy)

	return scanContest(row)
}

// ByID returns one contest with its languages and translations.
func (r *Contests) ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	return scanContest(r.querier(ctx).QueryRow(ctx,
		`SELECT `+contestColumns+` FROM contests c WHERE c.id = $1`, id))
}

// List returns a page of contests and the total matching the filter.
//
// The two scopes are applied here rather than by the caller because they are
// selection, not authorisation: ManagedBy is what keeps an organizer's list
// their own, and VisibleTo is what a student may see at all.
func (r *Contests) List(ctx context.Context, f contests.Filter) ([]contests.Contest, int, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+contestColumns+`, COUNT(*) OVER() AS total
		FROM contests c
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
		ORDER BY c.starts_at DESC NULLS LAST, c.created_at DESC
		LIMIT $5 OFFSET $6`,
		f.Query, f.Status, nilUUID(f.ManagedBy), nilUUID(f.VisibleTo), f.Limit, f.Offset)
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
		if err := rows.Scan(
			&c.ID, &c.Status, &c.Enrollment, &c.QuestionMode, &c.Timing, &c.DurationMin,
			&c.StartsAt, &c.EndsAt, &c.AllowedCIDRs, &settings, &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt, &languages, &translations, &total,
		); err != nil {
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
	return found, total, nil
}

// Update saves a contest's own fields.
//
// Status is deliberately not among them: it moves through the service's
// transition, which is where the lifecycle rules and the publish gate live,
// and a second way to set it would be a way around both.
func (r *Contests) Update(ctx context.Context, c contests.Contest) error {
	settings, err := json.Marshal(c.Settings)
	if err != nil {
		return fmt.Errorf("encode contest settings: %w", err)
	}

	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE contests
		SET enrollment = $2, question_mode = $3, timing = $4, duration_min = $5,
		    starts_at = $6, ends_at = $7, allowed_cidrs = $8, settings = $9,
		    updated_at = now()
		WHERE id = $1`,
		c.ID, c.Enrollment, c.QuestionMode, c.Timing, c.DurationMin,
		c.StartsAt, c.EndsAt, cidrList(c.AllowedCIDRs), settings)
	if err != nil {
		return fmt.Errorf("update contest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// SetStatus moves the contest along its lifecycle.
func (r *Contests) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`UPDATE contests SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("set contest status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// Delete removes a contest. Everything hanging off it goes with it through the
// schema's cascades, so there is nothing to clean up by hand.
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

	// Delete-then-insert in one statement each: "replace" has to mean replace,
	// and a language quietly left behind would keep the publish gate demanding
	// translations for something the contest no longer offers.
	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM contest_languages WHERE contest_id = $1 AND NOT (lang = ANY($2))`,
		id, codes); err != nil {
		return fmt.Errorf("replace contest languages: %w", err)
	}
	if len(codes) == 0 {
		return nil
	}

	// Clear the old default before writing the new one. Only one language per
	// contest may carry the flag, and that is a plain unique index — checked
	// row by row, and not deferrable. Without this step, moving the default
	// from en to ro collides with the en row that has not been rewritten yet,
	// and the move is simply impossible to express.
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
		return fmt.Errorf("replace contest languages: %w", err)
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

	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM contest_translations WHERE contest_id = $1 AND NOT (lang = ANY($2))`,
		id, langs); err != nil {
		return fmt.Errorf("replace contest translations: %w", err)
	}
	if len(langs) == 0 {
		return nil
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_translations (contest_id, lang, title, description)
		SELECT $1, lang, title, NULLIF(description, '')
		FROM unnest($2::text[], $3::text[], $4::text[]) AS t(lang, title, description)
		ON CONFLICT (contest_id, lang) DO UPDATE
		SET title = EXCLUDED.title, description = EXCLUDED.description, updated_at = now()`,
		id, langs, titles, descriptions)
	if err != nil {
		return fmt.Errorf("replace contest translations: %w", err)
	}
	return nil
}

// cidrList makes sure an empty restriction is stored as an empty array rather
// than NULL, which is what the column's NOT NULL default expects and what
// "no restriction" means everywhere else.
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
