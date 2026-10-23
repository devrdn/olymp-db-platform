package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	_ contests.SchemaNotesRepository = (*SchemaNotes)(nil)
	_ contests.SchemaNotesText       = (*SchemaNotes)(nil)
)

// SchemaNotes stores a contest's table descriptions, a row per language.
type SchemaNotes struct {
	pool *pgxpool.Pool
}

// NewSchemaNotes returns the table-descriptions repository.
func NewSchemaNotes(pool *pgxpool.Pool) *SchemaNotes {
	return &SchemaNotes{pool: pool}
}

func (r *SchemaNotes) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ByContest returns the contest's table descriptions in every language.
func (r *SchemaNotes) ByContest(ctx context.Context, contestID uuid.UUID) (contests.SchemaNotes, error) {
	var (
		bodies    []byte
		updatedAt *time.Time
	)
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT COALESCE(json_object_agg(lang, body_md), '{}'::json), max(updated_at)
		FROM contest_schema_notes WHERE contest_id = $1`, contestID).Scan(&bodies, &updatedAt)
	if err != nil {
		return contests.SchemaNotes{}, fmt.Errorf("load table descriptions: %w", err)
	}
	notes := contests.SchemaNotes{}
	if err := json.Unmarshal(bodies, &notes.Bodies); err != nil {
		return contests.SchemaNotes{}, fmt.Errorf("decode table descriptions: %w", err)
	}
	if updatedAt != nil {
		notes.UpdatedAt = *updatedAt
	}
	return notes, nil
}

// FirstIn reads the text in the first of langs that has one, by the primary
// key.
func (r *SchemaNotes) FirstIn(ctx context.Context, contestID uuid.UUID, langs []string) (string, error) {
	if len(langs) == 0 {
		return "", nil
	}
	var body string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT body_md FROM contest_schema_notes
		WHERE contest_id = $1 AND lang = ANY($2)
		ORDER BY array_position($2, lang)
		LIMIT 1`, contestID, langs).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load table descriptions text: %w", err)
	}
	return body, nil
}

// Save replaces the contest's table descriptions with exactly these
// languages.
func (r *SchemaNotes) Save(ctx context.Context, contestID uuid.UUID, bodies map[string]string) (contests.SchemaNotes, error) {
	langs := make([]string, 0, len(bodies))
	texts := make([]string, 0, len(bodies))
	for lang, body := range bodies {
		langs = append(langs, lang)
		texts = append(texts, body)
	}

	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM contest_schema_notes WHERE contest_id = $1 AND NOT (lang = ANY($2))`,
		contestID, langs); err != nil {
		return contests.SchemaNotes{}, fmt.Errorf("save table descriptions: %w", err)
	}
	if len(langs) > 0 {
		if _, err := r.querier(ctx).Exec(ctx, `
			INSERT INTO contest_schema_notes (contest_id, lang, body_md)
			SELECT $1, lang, body FROM unnest($2::text[], $3::text[]) AS t(lang, body)
			ON CONFLICT (contest_id, lang) DO UPDATE
			SET body_md = EXCLUDED.body_md, updated_at = now()`,
			contestID, langs, texts); err != nil {
			// The foreign key refuses a contest deleted since the caller read it.
			return contests.SchemaNotes{}, fmt.Errorf("save table descriptions: %w", missingParent(err, map[string]error{
				"contest_schema_notes_contest_id_fkey": contests.ErrNotFound,
			}))
		}
	}

	return r.ByContest(ctx, contestID)
}
