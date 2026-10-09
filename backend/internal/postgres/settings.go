package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings stores what an installation calls itself.
type Settings struct{ pool *pgxpool.Pool }

var _ settings.Repository = (*Settings)(nil)

// NewSettings returns a repository over pool.
func NewSettings(pool *pgxpool.Pool) *Settings { return &Settings{pool: pool} }

func (r *Settings) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// All returns every stored value, including keys this build does not know.
// Filtering them is the domain's job, kept in one place.
func (r *Settings) All(ctx context.Context) (settings.Values, error) {
	rows, err := r.querier(ctx).Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()

	values := settings.Values{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}

		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			// Skip a non-string value: one unreadable setting must not take
			// the sign-in screen down.
			continue
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return values, nil
}

// Save writes the values and stamps who did it, in one statement so a save
// cannot land half-applied. updated_by is NULL for a system actor.
func (r *Settings) Save(ctx context.Context, actorID uuid.UUID, values settings.Values) error {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	encoded := make([][]byte, 0, len(values))
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode setting %q: %w", key, err)
		}
		keys = append(keys, key)
		encoded = append(encoded, raw)
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO settings (key, value, updated_by, updated_at)
		SELECT k, v::jsonb, $3, now()
		FROM unnest($1::text[], $2::text[]) AS t(k, v)
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = EXCLUDED.updated_at`,
		keys, asText(encoded), nilUUID(actorID))
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

func asText(encoded [][]byte) []string {
	out := make([]string, 0, len(encoded))
	for _, raw := range encoded {
		out = append(out, string(raw))
	}
	return out
}

// SettingsImages stores the pictures an installation puts on itself.
type SettingsImages struct{ pool *pgxpool.Pool }

var _ settings.ImageRepository = (*SettingsImages)(nil)

// NewSettingsImages returns a repository over pool.
func NewSettingsImages(pool *pgxpool.Pool) *SettingsImages { return &SettingsImages{pool: pool} }

func (r *SettingsImages) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ByKind returns the picture in that slot.
func (r *SettingsImages) ByKind(ctx context.Context, kind string) (settings.Image, error) {
	var img settings.Image
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT id, kind, content_type, bytes, sha256, width, height, uploaded_at
		FROM settings_files WHERE kind = $1`, kind).
		Scan(&img.ID, &img.Kind, &img.ContentType, &img.Bytes, &img.SHA256,
			&img.Width, &img.Height, &img.UploadedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings.Image{}, settings.ErrImageNotFound
	}
	if err != nil {
		return settings.Image{}, fmt.Errorf("read image %q: %w", kind, err)
	}
	return img, nil
}

// Save replaces whatever is in the slot; the unique key on kind keeps one row
// per slot.
func (r *SettingsImages) Save(ctx context.Context, actorID uuid.UUID, img settings.Image) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO settings_files (kind, content_type, bytes, sha256, width, height, uploaded_by, uploaded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (kind) DO UPDATE
		SET content_type = EXCLUDED.content_type,
		    bytes = EXCLUDED.bytes,
		    sha256 = EXCLUDED.sha256,
		    width = EXCLUDED.width,
		    height = EXCLUDED.height,
		    uploaded_by = EXCLUDED.uploaded_by,
		    uploaded_at = EXCLUDED.uploaded_at`,
		img.Kind, img.ContentType, img.Bytes, img.SHA256, img.Width, img.Height, nilUUID(actorID))
	if err != nil {
		return fmt.Errorf("save image %q: %w", img.Kind, err)
	}
	return nil
}

// Delete empties the slot. An empty one is not an error.
func (r *SettingsImages) Delete(ctx context.Context, kind string) error {
	if _, err := r.querier(ctx).Exec(ctx, `DELETE FROM settings_files WHERE kind = $1`, kind); err != nil {
		return fmt.Errorf("remove image %q: %w", kind, err)
	}
	return nil
}

// Present lists the filled slots with the hash the URL carries, without
// reading the image bytes.
func (r *SettingsImages) Present(ctx context.Context) (map[string]string, error) {
	rows, err := r.querier(ctx).Query(ctx, `SELECT kind, sha256 FROM settings_files`)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	present := map[string]string{}
	for rows.Next() {
		var kind, sum string
		if err := rows.Scan(&kind, &sum); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		present[kind] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	return present, nil
}
