package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The game's lifecycle: the script an organiser writes and the build made from
// it. The pool of copies it produces lives in gameinstances.go.

const templateColumns = `contest_id, template_db, version, status,
	init_script, coalesce(build_error, ''), updated_at, source, upload_id, definition_json, data_changed_at`

func scanTemplate(row pgx.Row) (provisioning.Template, error) {
	var t provisioning.Template
	var status, source string
	var definitionJSON []byte
	err := row.Scan(&t.ContestID, &t.Database, &t.Version, &status, &t.Script, &t.BuildError, &t.UpdatedAt,
		&source, &t.UploadID, &definitionJSON, &t.DataChangedAt)
	if err != nil {
		return t, err
	}
	t.Status = provisioning.TemplateStatus(status)
	t.Source = provisioning.TemplateSource(source)
	// Set here too, so ScriptBytes is valid whichever read produced the row.
	t.ScriptBytes = len(t.Script)
	if len(definitionJSON) > 0 {
		if err := json.Unmarshal(definitionJSON, &t.Definition); err != nil {
			return t, fmt.Errorf("decode the game's definition: %w", err)
		}
	}
	return t, nil
}

// templateStatusColumns is templateColumns without the content: octet_length
// replaces init_script and definition_json is left out. octet_length equals
// len() of the Go string, so ScriptBytes agrees between the two reads.
const templateStatusColumns = `contest_id, template_db, version, status,
	octet_length(init_script), coalesce(build_error, ''), updated_at, source, upload_id, data_changed_at`

func scanTemplateStatus(row pgx.Row) (provisioning.Template, error) {
	var t provisioning.Template
	var status, source string
	err := row.Scan(&t.ContestID, &t.Database, &t.Version, &status, &t.ScriptBytes, &t.BuildError,
		&t.UpdatedAt, &source, &t.UploadID, &t.DataChangedAt)
	if err != nil {
		return t, err
	}
	t.Status = provisioning.TemplateStatus(status)
	t.Source = provisioning.TemplateSource(source)
	return t, nil
}

// upsertGame is the one statement that replaces a contest's game, whether from
// a script, an upload or a builder definition. Each bumps the version, clears
// the cached schema and puts the game back to pending.
//
// definitionJSON is nil for non-builder sources, as uploadID is for non-file
// ones. The upsert overwrites both unconditionally, so replacing a game clears
// what no longer applies (migration 26 CHECKs the pairing).
func (r *GameInstances) upsertGame(
	ctx context.Context, contestID uuid.UUID, database, script, source string, uploadID *uuid.UUID, definitionJSON []byte,
) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_templates (contest_id, template_db, init_script, source, upload_id, definition_json, status, version)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', 1)
		ON CONFLICT (contest_id) DO UPDATE
		SET init_script     = EXCLUDED.init_script,
		    template_db     = EXCLUDED.template_db,
		    source          = EXCLUDED.source,
		    upload_id       = EXCLUDED.upload_id,
		    definition_json = EXCLUDED.definition_json,
		    status          = 'pending',
		    build_error     = NULL,
		    version         = game_templates.version + 1,
		    schema_json     = NULL,
		    schema_version  = NULL,
		    updated_at      = now()
		RETURNING `+templateColumns, contestID, database, script, source, uploadID, definitionJSON))
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("store the game: %w", err)
	}
	return template, nil
}

// SaveScript stores one contest's game script and puts the game back to
// pending. Raising the version makes every existing copy stale, so the pool
// tender drops and remakes them. The cached schema is cleared with it; its two
// columns must be null together (migration 22).
func (r *GameInstances) SaveScript(ctx context.Context, contestID uuid.UUID, database, script string) (provisioning.Template, error) {
	return r.upsertGame(ctx, contestID, database, script, string(provisioning.SourceEditor), nil, nil)
}

// SaveDefinition stores one contest's game as a builder definition, encoded as
// jsonb with an empty script, and puts the game back to pending. The caller
// must have run Definition.Validate.
func (r *GameInstances) SaveDefinition(
	ctx context.Context, contestID uuid.UUID, database string, definition provisioning.Definition,
) (provisioning.Template, error) {
	document, err := json.Marshal(definition)
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("encode the game definition: %w", err)
	}
	return r.upsertGame(ctx, contestID, database, "", string(provisioning.SourceBuilder), nil, document)
}

// Template reads one contest's game, or ErrNoGame when it has none yet.
func (r *GameInstances) Template(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx,
		`SELECT `+templateColumns+` FROM game_templates WHERE contest_id = $1`, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("read the contest's game: %w", err)
	}
	return template, nil
}

// TemplateStatus reads one contest's game without its content, for a console
// polling a running build.
func (r *GameInstances) TemplateStatus(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := scanTemplateStatus(r.querier(ctx).QueryRow(ctx,
		`SELECT `+templateStatusColumns+` FROM game_templates WHERE contest_id = $1`, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("read the contest's game: %w", err)
	}
	return template, nil
}

// ClaimBuild takes one game waiting to be built and marks it building. FOR
// UPDATE SKIP LOCKED keeps two workers from building one database and lets
// the second take the next row without waiting.
//
// A game left building for longer than stale is claimed too, so a build whose
// process died is retried. Nothing else touches updated_at during a build, so
// its age is the build's duration.
func (r *GameInstances) ClaimBuild(ctx context.Context, stale time.Duration) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx, `
		UPDATE game_templates SET status = 'building', updated_at = now()
		WHERE contest_id = (
			SELECT contest_id FROM game_templates
			WHERE status = 'pending'
			   OR (status = 'building' AND updated_at < now() - $1::interval)
			ORDER BY updated_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+templateColumns, stale))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("claim a game to build: %w", err)
	}
	return template, nil
}

// FinishBuild records how the build of one version ended. The version check
// discards the outcome of a build that a newer save has superseded.
func (r *GameInstances) FinishBuild(
	ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time,
) error {
	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_templates
		SET status          = CASE WHEN $3 = '' THEN 'ready' ELSE 'failed' END,
		    build_error     = nullif($3, ''),
		    -- Only a build that succeeded, and only a change it could have
		    -- seen. A row added while the build ran moved the mark past $4,
		    -- and clearing it here would lose that row for good: no later
		    -- build would know to look. A build that failed clears nothing at
		    -- all — its template is dropped, so the data never reached a
		    -- database, and this column is the only record that it has not.
		    data_changed_at = CASE
		        WHEN $3 = '' AND data_changed_at <= $4 THEN NULL
		        ELSE data_changed_at
		    END,
		    updated_at      = now()
		WHERE contest_id = $1 AND version = $2 AND status = 'building'`,
		contestID, version, buildError, claimedAt); err != nil {
		return fmt.Errorf("record the build's outcome: %w", err)
	}
	return nil
}

// RequestBuild puts a finished game back to pending and raises its version,
// leaving the stored content untouched. The status condition settles a race
// between two requests: only one raises the version, and the other gets
// ErrBuildInProgress. The cached schema is cleared as in upsertGame.
func (r *GameInstances) RequestBuild(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx, `
		UPDATE game_templates
		SET status         = 'pending',
		    version        = version + 1,
		    build_error    = NULL,
		    schema_json    = NULL,
		    schema_version = NULL,
		    updated_at     = now()
		WHERE contest_id = $1 AND status IN ('ready', 'failed')
		RETURNING `+templateColumns, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Template{}, provisioning.ErrBuildInProgress
	}
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("ask for the game to be built: %w", err)
	}
	return template, nil
}

// Policy is a contest's SQL policy, which the build grants inside the
// template. It does not go through Game, which answers only for a ready
// template and so never during a first build.
func (r *GameInstances) Policy(ctx context.Context, contestID uuid.UUID) (sqlpolicy.Policy, error) {
	var policy sqlpolicy.Policy
	var mode string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT `+policyProjectionColumns+`
		FROM contests c
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE c.id = $1`, contestID).
		Scan(policyScanTargets(&policy, &mode)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlpolicy.Policy{}, provisioning.ErrNoGame
	}
	if err != nil {
		return sqlpolicy.Policy{}, fmt.Errorf("read the contest's SQL policy: %w", err)
	}
	policy.Mode = sqlpolicy.Mode(mode)
	return policy, nil
}

// GameEditable reports whether a contest's game may still be replaced. It
// repeats contests.Contest.ContentEditable in SQL so provisioning need not
// import contests; a test keeps the two in step.
func (r *GameInstances) GameEditable(ctx context.Context, contestID uuid.UUID) (bool, error) {
	var editable bool
	err := r.querier(ctx).QueryRow(ctx,
		`SELECT status IN ('draft', 'published') FROM contests WHERE id = $1`, contestID).Scan(&editable)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, provisioning.ErrNoGame
	}
	if err != nil {
		return false, fmt.Errorf("read the contest's status: %w", err)
	}
	return editable, nil
}
