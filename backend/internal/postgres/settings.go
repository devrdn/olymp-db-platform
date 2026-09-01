package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
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

// All returns every stored value.
//
// Whatever keys the table happens to hold, including ones this build knows
// nothing about — a row left behind by a version that has been rolled back is
// still a row. Deciding which of them mean anything is the domain's job, and
// keeping that decision in one place is what stops a forgotten key leaking
// through a reader that did not think to filter.
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
			// A value written as something other than a string is a row this
			// build cannot use. Skipped rather than fatal: one unreadable
			// setting must not take the sign-in screen down with it.
			continue
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return values, nil
}

// Save writes the values and stamps who did it.
//
// One statement for the whole set, so a save is one round trip and cannot land
// half-applied on its own account. `updated_by` is nullable and set to NULL
// for a system actor, which is what the column's ON DELETE SET NULL already
// promises: the record outlives the account that made it.
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

// asText turns encoded values into the text array the statement unnests.
func asText(encoded [][]byte) []string {
	out := make([]string, 0, len(encoded))
	for _, raw := range encoded {
		out = append(out, string(raw))
	}
	return out
}
