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

// The cached shape of a contest's game: the schema a participant's console
// draws its table list from, kept on the game_templates row beside the build
// it describes (migration 22's own pair of columns).
//
// Its own file rather than more of gameinstances.go, and not because that
// file had grown: this is the one part of GameInstances that answers about
// *derived* data — a document this package can throw away and read again
// from the cluster — where everything else there answers about a database
// that exists or does not. gameschema_test.go is named for this file, and
// used to be named for a file that did not exist.

// CachedSchema reads the shape worked out for this contest's game, and which
// template build it describes.
//
// ErrNoSchema covers both "never worked out" and "the contest has no game
// row at all": neither is a failure, and both mean the same thing to the
// caller — ask the cluster. The version comes back beside the document rather
// than being compared here, because deciding whether a cached build is still
// the current one is the reader's own rule (provisioning.SchemaReader), and
// having two places that know it is how the two drift.
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
		// A document this build cannot read is not worth failing over: it is
		// derived data, and the cluster still knows the truth. Reported as
		// "not cached" so the caller reads it again and overwrites this.
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	return schema, version, nil
}

// SaveSchema records the shape, against the template build it describes.
//
// A plain UPDATE, and deliberately not an upsert: the row belongs to the
// contest's game and is created when the game is. A contest with no game has
// no schema to cache, and inventing a game_templates row here would create
// one with no template_db and no init_script.
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
