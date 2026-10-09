package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stories implements contests.StoryRepository, and the participant's
// contests.StoryText beside it.
var (
	_ contests.StoryRepository = (*Stories)(nil)
	_ contests.StoryText       = (*Stories)(nil)
)

// Stories stores the crime story of a contest.
type Stories struct {
	pool *pgxpool.Pool
}

// NewStories returns the story repository.
func NewStories(pool *pgxpool.Pool) *Stories {
	return &Stories{pool: pool}
}

func (r *Stories) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ByContest returns the contest's story with its text in every language.
func (r *Stories) ByContest(ctx context.Context, contestID uuid.UUID) (contests.Story, error) {
	var (
		story  contests.Story
		bodies []byte
	)
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT s.id, s.contest_id, s.updated_at,
		       COALESCE((
		           SELECT json_object_agg(st.lang, st.body_md)
		           FROM story_translations st WHERE st.story_id = s.id
		       ), '{}'::json)
		FROM stories s WHERE s.contest_id = $1`, contestID).
		Scan(&story.ID, &story.ContestID, &story.UpdatedAt, &bodies)

	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Story{}, contests.ErrStoryNotFound
	}
	if err != nil {
		return contests.Story{}, fmt.Errorf("load story: %w", err)
	}
	if err := json.Unmarshal(bodies, &story.Bodies); err != nil {
		return contests.Story{}, fmt.Errorf("decode story translations: %w", err)
	}
	return story, nil
}

// BodyIn returns the story's text in one language, by the translations'
// primary key.
func (r *Stories) BodyIn(ctx context.Context, contestID uuid.UUID, lang string) (string, error) {
	var body string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT st.body_md
		FROM stories s
		JOIN story_translations st ON st.story_id = s.id
		WHERE s.contest_id = $1 AND st.lang = $2`, contestID, lang).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", contests.ErrStoryNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load story text: %w", err)
	}
	return body, nil
}

// Save creates or replaces the story; a contest has no story row until one is
// written.
func (r *Stories) Save(ctx context.Context, contestID uuid.UUID, bodies map[string]string) (contests.Story, error) {
	var storyID uuid.UUID
	err := r.querier(ctx).QueryRow(ctx, `
		INSERT INTO stories (contest_id, updated_at) VALUES ($1, now())
		ON CONFLICT (contest_id) DO UPDATE SET updated_at = now()
		RETURNING id`, contestID).Scan(&storyID)
	if err != nil {
		// The foreign key refuses a contest deleted since the caller read it.
		return contests.Story{}, fmt.Errorf("save story: %w", missingParent(err, map[string]error{
			"stories_contest_id_fkey": contests.ErrNotFound,
		}))
	}

	langs := make([]string, 0, len(bodies))
	texts := make([]string, 0, len(bodies))
	for lang, body := range bodies {
		langs = append(langs, lang)
		texts = append(texts, body)
	}

	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM story_translations WHERE story_id = $1 AND NOT (lang = ANY($2))`,
		storyID, langs); err != nil {
		return contests.Story{}, fmt.Errorf("save story translations: %w", err)
	}
	if len(langs) > 0 {
		if _, err := r.querier(ctx).Exec(ctx, `
			INSERT INTO story_translations (story_id, lang, body_md)
			SELECT $1, lang, body FROM unnest($2::text[], $3::text[]) AS t(lang, body)
			ON CONFLICT (story_id, lang) DO UPDATE
			SET body_md = EXCLUDED.body_md, updated_at = now()`,
			storyID, langs, texts); err != nil {
			return contests.Story{}, fmt.Errorf("save story translations: %w", err)
		}
	}

	return r.ByContest(ctx, contestID)
}

// Delete removes the story of a contest.
func (r *Stories) Delete(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx, `DELETE FROM stories WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("delete story: %w", err)
	}
	return nil
}
