package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The game's own lifecycle: the script an organiser writes and the build made
// from it. Apart from gameinstances.go, which is the pool of copies that
// lifecycle produces — one table, two jobs, and they run at different times
// in a contest's life.

// templateColumns is the row every read below returns, in one place so the
// three of them cannot drift.
const templateColumns = `contest_id, template_db, version, status,
	init_script, coalesce(build_error, ''), updated_at`

func scanTemplate(row pgx.Row) (provisioning.Template, error) {
	var t provisioning.Template
	var status string
	err := row.Scan(&t.ContestID, &t.Database, &t.Version, &status, &t.Script, &t.BuildError, &t.UpdatedAt)
	t.Status = provisioning.TemplateStatus(status)
	return t, err
}

// SaveScript stores one contest's game script and puts the game back to
// pending.
//
// An upsert that bumps the version, which is the whole mechanism behind a
// rebuild: every instance carries the version it was copied from, so raising
// it is what makes the existing copies stale and what has the pool tender
// drop and remake them. It is also why provisioning.Games refuses this for a
// contest that is already running.
//
// The cached schema is cleared in the same statement. It describes the build
// being replaced, and the two columns are constrained to be null together
// (migration 22).
func (r *GameInstances) SaveScript(ctx context.Context, contestID uuid.UUID, database, script string) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_templates (contest_id, template_db, init_script, status, version)
		VALUES ($1, $2, $3, 'pending', 1)
		ON CONFLICT (contest_id) DO UPDATE
		SET init_script    = EXCLUDED.init_script,
		    template_db    = EXCLUDED.template_db,
		    status         = 'pending',
		    build_error    = NULL,
		    version        = game_templates.version + 1,
		    schema_json    = NULL,
		    schema_version = NULL,
		    updated_at     = now()
		RETURNING `+templateColumns, contestID, database, script))
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("store the game script: %w", err)
	}
	return template, nil
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

// ClaimBuild takes one game waiting to be built and marks it building.
//
// The conditional update is the race arbiter, the same one ClaimSpare uses
// for a copy: two workers ticking at the same moment must not both run
// CREATE DATABASE against one name. `FOR UPDATE SKIP LOCKED` is what makes
// the second one take the next row rather than wait for the first.
//
// A game left in `building` for longer than stale is claimed too. An API that
// died mid-build would otherwise leave it there for ever, with an organiser
// watching a spinner that will never stop — and `updated_at` is touched by
// nothing else while a build runs, so its age is exactly how long the build
// has been going.
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

// FinishBuild records how the build of one version ended.
//
// `version = $2` is what keeps a slow build from speaking for a newer script:
// saving a script while a build runs already bumped the version, so the older
// build's outcome matches nothing and is dropped rather than marking the new
// script ready — or, worse, marking it failed with the old script's error.
func (r *GameInstances) FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string) error {
	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_templates
		SET status      = CASE WHEN $3 = '' THEN 'ready' ELSE 'failed' END,
		    build_error = nullif($3, ''),
		    updated_at  = now()
		WHERE contest_id = $1 AND version = $2 AND status = 'building'`,
		contestID, version, buildError); err != nil {
		return fmt.Errorf("record the build's outcome: %w", err)
	}
	return nil
}

// Policy is what a contest lets participants do, which is what the build
// grants inside the template.
//
// The same coalesced defaults Game() reads, and deliberately a separate query
// rather than a call to it: Game() answers only for a template that is
// already 'ready', which is precisely the state a build is not in, so reading
// the policy through it would have meant every first build granting the
// defaults instead of what the organiser configured.
func (r *GameInstances) Policy(ctx context.Context, contestID uuid.UUID) (sqlpolicy.Policy, error) {
	var policy sqlpolicy.Policy
	var mode string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT coalesce(p.mode, 'read_only'),
		       coalesce(p.writable_tables, '{}')::text[],
		       coalesce(p.allow_create_view, false),
		       coalesce(p.allow_own_tables, false),
		       coalesce(p.allow_temp_tables, false),
		       coalesce(p.allow_catalog, true),
		       coalesce(p.disk_quota_ratio, 5)
		FROM contests c
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE c.id = $1`, contestID).
		Scan(&mode, &policy.WritableTables, &policy.AllowCreateView,
			&policy.AllowOwnTables, &policy.AllowTempTables, &policy.AllowCatalog,
			&policy.DiskQuotaRatio)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlpolicy.Policy{}, provisioning.ErrNoGame
	}
	if err != nil {
		return sqlpolicy.Policy{}, fmt.Errorf("read the contest's SQL policy: %w", err)
	}
	policy.Mode = sqlpolicy.Mode(mode)
	return policy, nil
}

// GameEditable reports whether a contest's game may still be replaced.
//
// The rule is contests.Contest.ContentEditable — a draft or a published
// contest and nothing later — spelled once here as SQL so that provisioning
// can ask without importing the contests package. gametemplates_test.go walks
// every status and insists the two agree, which is the only thing that stops
// them drifting.
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
