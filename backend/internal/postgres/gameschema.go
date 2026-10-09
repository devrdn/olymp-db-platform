package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The cached schema of a contest's game lives on its game_templates row. It is
// derived data that can be dropped and read again from the cluster.

// CachedSchema reads the cached schema and the template version it describes;
// the caller decides whether that version is current. ErrNoSchema means never
// cached or no game row.
func (r *GameInstances) CachedSchema(ctx context.Context, contestID uuid.UUID) (provisioning.Schema, int, error) {
	var (
		document []byte
		version  int
	)
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT schema_json, schema_version
		FROM game_templates
		WHERE contest_id = $1 AND schema_json IS NOT NULL`, contestID).Scan(&document, &version)

	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	if err != nil {
		return provisioning.Schema{}, 0, fmt.Errorf("read the game's cached schema: %w", err)
	}

	var schema provisioning.Schema
	if err := json.Unmarshal(document, &schema); err != nil {
		// Unreadable derived data reads as not cached, so the caller fetches
		// it again and overwrites this.
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	return schema, version, nil
}

// SaveSchema caches the schema against the template version it describes. It
// is an UPDATE, not an upsert: a row created here would have no template_db
// or init_script.
func (r *GameInstances) SaveSchema(ctx context.Context, contestID uuid.UUID, version int, schema provisioning.Schema) error {
	document, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode the game's schema: %w", err)
	}
	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_templates
		SET schema_json = $2, schema_version = $3
		WHERE contest_id = $1`, contestID, document, version); err != nil {
		return fmt.Errorf("cache the game's schema: %w", err)
	}
	return nil
}
