package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GameInstances records which database belongs to whom. A spare copy is a row
// with no registration, so claiming one is a single UPDATE rather than a
// delete and an insert.
type GameInstances struct{ pool *pgxpool.Pool }

var _ provisioning.Repository = (*GameInstances)(nil)

// NewGameInstances returns a repository over pool.
func NewGameInstances(pool *pgxpool.Pool) *GameInstances { return &GameInstances{pool: pool} }

func (r *GameInstances) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ClaimSpare hands one free copy to a registration, or returns
// provisioning.ErrNoSpare. FOR UPDATE SKIP LOCKED lets concurrent claimants
// take different copies without blocking; without the lock two claimants
// could take the same one.
func (r *GameInstances) ClaimSpare(ctx context.Context, contest, registration uuid.UUID, version int) (string, error) {
	var database string
	err := r.querier(ctx).QueryRow(ctx, `
		UPDATE game_instances
		SET registration_id = $2, updated_at = now()
		WHERE id = (
			SELECT id FROM game_instances
			WHERE contest_id = $1
			  AND registration_id IS NULL
			  AND status = 'ready'
			  AND template_version = $3
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING db_name`,
		contest, registration, version).Scan(&database)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", provisioning.ErrNoSpare
	}
	if err != nil {
		return "", fmt.Errorf("claim a spare copy: %w", err)
	}
	return database, nil
}

// AddSpare records a copy that belongs to the contest and to nobody yet.
func (r *GameInstances) AddSpare(ctx context.Context, contest uuid.UUID, database string, version int) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO game_instances (contest_id, db_name, template_version, status)
		VALUES ($1, $2, $3, 'ready')`,
		contest, database, version)
	if err != nil {
		return fmt.Errorf("record the spare copy %s: %w", database, err)
	}
	return nil
}

// Assign records a database created directly for one participant, when the
// pool was empty.
func (r *GameInstances) Assign(ctx context.Context, contest, registration uuid.UUID, database string, version int) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO game_instances (contest_id, registration_id, db_name, template_version, status)
		VALUES ($1, $2, $3, $4, 'ready')
		ON CONFLICT (registration_id) DO UPDATE
		SET db_name = EXCLUDED.db_name,
		    template_version = EXCLUDED.template_version,
		    status = 'ready',
		    updated_at = now()`,
		contest, registration, database, version)
	if err != nil {
		return fmt.Errorf("assign %s: %w", database, err)
	}
	return nil
}

// Of returns the database a registration already has, if any.
func (r *GameInstances) Of(ctx context.Context, registration uuid.UUID) (provisioning.Instance, error) {
	var instance provisioning.Instance
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT db_name, template_version, status
		FROM game_instances WHERE registration_id = $1`, registration).
		Scan(&instance.Database, &instance.TemplateVersion, &instance.Status)

	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Instance{}, provisioning.ErrNoInstance
	}
	if err != nil {
		return provisioning.Instance{}, fmt.Errorf("read the instance: %w", err)
	}
	return instance, nil
}

// Stale lists a contest's claimed and spare databases that came from an older
// template.
func (r *GameInstances) Stale(ctx context.Context, contest uuid.UUID, version int) ([]provisioning.Stale, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT db_name, registration_id
		FROM game_instances
		WHERE contest_id = $1 AND template_version < $2 AND status <> 'dropped'
		ORDER BY created_at`, contest, version)
	if err != nil {
		return nil, fmt.Errorf("list stale instances: %w", err)
	}
	defer rows.Close()

	var out []provisioning.Stale
	for rows.Next() {
		var s provisioning.Stale
		if err := rows.Scan(&s.Database, &s.Registration); err != nil {
			return nil, fmt.Errorf("scan a stale instance: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list stale instances: %w", err)
	}
	return out, nil
}

// Forget removes the record of a database that no longer exists.
func (r *GameInstances) Forget(ctx context.Context, database string) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM game_instances WHERE db_name = $1`, database); err != nil {
		return fmt.Errorf("forget %s: %w", database, err)
	}
	return nil
}

// SpareCount is how many ready spare copies exist for one version of a
// contest's template.
func (r *GameInstances) SpareCount(ctx context.Context, contest uuid.UUID, version int) (int, error) {
	var spare int
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*) FROM game_instances
		WHERE contest_id = $1 AND registration_id IS NULL
		  AND status = 'ready' AND template_version = $2`, contest, version).Scan(&spare)
	if err != nil {
		return 0, fmt.Errorf("count spare copies: %w", err)
	}
	return spare, nil
}

// WaitingParticipants counts a contest's non-disqualified registrations that
// hold no current copy (one not dropped and built from template version
// `version` or later); the pool depth is sized from it. A stale copy does not
// count, since Invalidate is about to remove it.
func (r *GameInstances) WaitingParticipants(ctx context.Context, contest uuid.UUID, version int) (int, error) {
	var waiting int
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*)
		FROM registrations r
		WHERE r.contest_id = $1
		  AND r.status <> 'disqualified'
		  AND NOT EXISTS (
		      SELECT 1 FROM game_instances g
		      WHERE g.registration_id = r.id
		        AND g.status <> 'dropped'
		        AND g.template_version >= $2
		  )`, contest, version).Scan(&waiting)
	if err != nil {
		return 0, fmt.Errorf("count participants without a copy: %w", err)
	}
	return waiting, nil
}

// AllCurrent reports whether every instance of a contest came from the current
// template, a precondition for starting it.
func (r *GameInstances) AllCurrent(ctx context.Context, contest uuid.UUID, version int) (bool, error) {
	var behind int
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*) FROM game_instances
		WHERE contest_id = $1 AND template_version < $2 AND status <> 'dropped'`,
		contest, version).Scan(&behind)
	if err != nil {
		return false, fmt.Errorf("check instance versions: %w", err)
	}
	return behind == 0, nil
}

// policyProjectionColumns reads a contest's SQL policy from a LEFT JOIN on
// contest_sql_policies p. coalesce gives a contest with no policy row the
// read-only default rather than no restrictions. Every query that reads a
// contest's game shares it so the default cannot drift.
const policyProjectionColumns = `
	coalesce(p.mode, 'read_only'),
	coalesce(p.writable_tables, '{}')::text[],
	coalesce(p.allow_create_view, false),
	coalesce(p.allow_own_tables, false),
	coalesce(p.allow_temp_tables, false),
	coalesce(p.allow_catalog, true),
	coalesce(p.disk_quota_ratio, 5)`

// policyScanTargets matches policyProjectionColumns in order. The mode lands
// as text in mode for the caller to convert.
func policyScanTargets(p *sqlpolicy.Policy, mode *string) []any {
	return []any{mode, &p.WritableTables, &p.AllowCreateView, &p.AllowOwnTables,
		&p.AllowTempTables, &p.AllowCatalog, &p.DiskQuotaRatio}
}

// Game returns one contest's template, version and policy. A template that is
// not ready yet gives provisioning.ErrNoGame.
func (r *GameInstances) Game(ctx context.Context, contestID uuid.UUID) (provisioning.Contest, error) {
	var c provisioning.Contest
	var mode string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT c.id, t.template_db, t.version,
		       `+policyProjectionColumns+`
		FROM contests c
		JOIN game_templates t ON t.contest_id = c.id
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE c.id = $1 AND t.status = 'ready'`, contestID).
		Scan(append([]any{&c.ID, &c.Template, &c.Version}, policyScanTargets(&c.Policy, &mode)...)...)

	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Contest{}, provisioning.ErrNoGame
	}
	if err != nil {
		return provisioning.Contest{}, fmt.Errorf("read the contest's game: %w", err)
	}
	c.Policy.Mode = sqlpolicy.Mode(mode)
	return c, nil
}

// Live lists the published or running contests with a ready template, whose
// pool is worth keeping stocked.
func (r *GameInstances) Live(ctx context.Context) ([]provisioning.Contest, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT c.id, t.template_db, t.version,
		       `+policyProjectionColumns+`
		FROM contests c
		JOIN game_templates t ON t.contest_id = c.id
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE c.status IN ('published', 'running') AND t.status = 'ready'
		ORDER BY c.id`)
	if err != nil {
		return nil, fmt.Errorf("list live contests: %w", err)
	}
	defer rows.Close()

	var out []provisioning.Contest
	for rows.Next() {
		var c provisioning.Contest
		var mode string
		if err := rows.Scan(append([]any{&c.ID, &c.Template, &c.Version}, policyScanTargets(&c.Policy, &mode)...)...); err != nil {
			return nil, fmt.Errorf("scan a live contest: %w", err)
		}
		c.Policy.Mode = sqlpolicy.Mode(mode)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live contests: %w", err)
	}
	return out, nil
}

// reclaimDeadline is when a finished or archived contest's grace period runs
// out, measured from updated_at, which the move to finished or archived sets
// in the same statement. It uses the database clock, not this process's.
// settings.grace_period_min wins when set; otherwise $1, the installation's
// default, applies.
const reclaimDeadline = `c.updated_at + make_interval(mins => CASE
	        WHEN coalesce((c.settings->>'grace_period_min')::int, 0) > 0
	          THEN (c.settings->>'grace_period_min')::int
	        ELSE $1
	      END)`

// Reclaimable lists up to limit not-yet-dropped instances of a contest that
// reached 'finished' or 'archived' longer ago than its grace period allows.
// Archived counts too, or archiving a finished contest would exempt its
// instances from the sweep forever.
//
// Ordering by deadline first spends the limit on the oldest debt; ordering by
// contest_id first would let one contest take every batch until it drains.
func (r *GameInstances) Reclaimable(ctx context.Context, installationGraceMin, limit int) ([]provisioning.ReclaimCandidate, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH candidates AS (
			SELECT i.db_name, i.contest_id, i.registration_id, i.created_at,
			       `+reclaimDeadline+` AS deadline
			FROM game_instances i
			JOIN contests c ON c.id = i.contest_id
			WHERE c.status IN ('finished', 'archived')
			  AND i.status <> 'dropped'
		)
		SELECT db_name, contest_id, registration_id, deadline
		FROM candidates
		WHERE deadline <= now()
		ORDER BY deadline, contest_id, created_at
		LIMIT $2`, installationGraceMin, limit)
	if err != nil {
		return nil, fmt.Errorf("list reclaimable instances: %w", err)
	}
	defer rows.Close()

	var out []provisioning.ReclaimCandidate
	for rows.Next() {
		var c provisioning.ReclaimCandidate
		if err := rows.Scan(&c.Database, &c.ContestID, &c.Registration, &c.Deadline); err != nil {
			return nil, fmt.Errorf("scan a reclaimable instance: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reclaimable instances: %w", err)
	}
	return out, nil
}

// MarkDropped moves one instance to the terminal 'dropped' status. It updates
// rather than deletes so the audit trail can still point at the row.
func (r *GameInstances) MarkDropped(ctx context.Context, database string) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_instances SET status = 'dropped', updated_at = now() WHERE db_name = $1`, database); err != nil {
		return fmt.Errorf("mark %s dropped: %w", database, err)
	}
	return nil
}

// ReclaimableTemplates lists up to limit contest templates that may be
// dropped: the contest is past its grace period as in Reclaimable, the
// template is 'ready' (anything else has nothing on disk), and every instance
// copied from it is already dropped. Called after the instances are dropped,
// it can reclaim a template in the same tick as its last instance.
func (r *GameInstances) ReclaimableTemplates(ctx context.Context, installationGraceMin, limit int) ([]provisioning.TemplateCandidate, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH candidates AS (
			SELECT t.contest_id, t.template_db,
			       `+reclaimDeadline+` AS deadline
			FROM game_templates t
			JOIN contests c ON c.id = t.contest_id
			WHERE c.status IN ('finished', 'archived')
			  AND t.status = 'ready'
			  AND NOT EXISTS (
			        SELECT 1 FROM game_instances i
			        WHERE i.contest_id = t.contest_id AND i.status <> 'dropped'
			      )
		)
		SELECT contest_id, template_db
		FROM candidates
		WHERE deadline <= now()
		ORDER BY deadline, contest_id
		LIMIT $2`, installationGraceMin, limit)
	if err != nil {
		return nil, fmt.Errorf("list reclaimable templates: %w", err)
	}
	defer rows.Close()

	var out []provisioning.TemplateCandidate
	for rows.Next() {
		var t provisioning.TemplateCandidate
		if err := rows.Scan(&t.ContestID, &t.Database); err != nil {
			return nil, fmt.Errorf("scan a reclaimable template: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reclaimable templates: %w", err)
	}
	return out, nil
}

// instanceColumns is the query Instances and InstanceNamed share. The joins
// are LEFT because a spare copy has no registration and a registration's
// account may have been deleted; coalesce turns both into empty strings.
const instanceColumns = `
	SELECT i.db_name, i.registration_id, i.template_version, i.status,
	       i.created_at, i.updated_at,
	       coalesce(u.login, ''), coalesce(u.full_name, '')
	FROM game_instances i
	LEFT JOIN registrations r ON r.id = i.registration_id
	LEFT JOIN users u ON u.id = r.user_id`

func scanInstance(row pgx.Row) (provisioning.InstanceRecord, error) {
	var r provisioning.InstanceRecord
	err := row.Scan(&r.Database, &r.Registration, &r.TemplateVersion, &r.Status,
		&r.CreatedAt, &r.UpdatedAt, &r.ParticipantLogin, &r.ParticipantName)
	return r, err
}

// Instances lists up to limit of one contest's databases, oldest first,
// including dropped ones so an organizer can see what happened to them.
// game_instances_contest_version_idx serves the filter by its leading column
// (CLAUDE.md rule 7).
func (r *GameInstances) Instances(ctx context.Context, contest uuid.UUID, limit int) ([]provisioning.InstanceRecord, error) {
	rows, err := r.querier(ctx).Query(ctx, instanceColumns+`
		WHERE i.contest_id = $1
		ORDER BY i.created_at, i.db_name
		LIMIT $2`, contest, limit)
	if err != nil {
		return nil, fmt.Errorf("list the contest's databases: %w", err)
	}
	defer rows.Close()

	var out []provisioning.InstanceRecord
	for rows.Next() {
		record, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan a database row: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the contest's databases: %w", err)
	}
	return out, nil
}

// InstanceNamed reads one of a contest's rows by database name. The contest
// stays in the WHERE clause although db_name is unique: the caller's
// permission covers one contest, and a lookup by name alone would let it act
// on another contest's database.
func (r *GameInstances) InstanceNamed(ctx context.Context, contest uuid.UUID, database string) (provisioning.InstanceRecord, error) {
	record, err := scanInstance(r.querier(ctx).QueryRow(ctx, instanceColumns+`
		WHERE i.contest_id = $1 AND i.db_name = $2`, contest, database))

	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.InstanceRecord{}, provisioning.ErrInstanceNotFound
	}
	if err != nil {
		return provisioning.InstanceRecord{}, fmt.Errorf("read the database row: %w", err)
	}
	return record, nil
}

// RecordedDatabases lists every instance and template row with its status,
// instances first, by name within each.
//
// It is unbounded on purpose. The orphan sweep destroys a database only when
// no row calls it live, so a truncated list could offer a live database for
// removal. It runs by hand, off the request path.
func (r *GameInstances) RecordedDatabases(ctx context.Context) ([]provisioning.DatabaseRecord, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT db_name, status, contest_id, false AS is_template FROM game_instances
		UNION ALL
		SELECT template_db, status, contest_id, true FROM game_templates
		ORDER BY is_template, 1`)
	if err != nil {
		return nil, fmt.Errorf("list the recorded databases: %w", err)
	}
	defer rows.Close()

	var out []provisioning.DatabaseRecord
	for rows.Next() {
		var record provisioning.DatabaseRecord
		if err := rows.Scan(&record.Database, &record.Status, &record.ContestID, &record.Template); err != nil {
			return nil, fmt.Errorf("scan a recorded database: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the recorded databases: %w", err)
	}
	return out, nil
}

// MarkTemplateDropped moves one contest's template to the terminal 'dropped'
// status, keeping the row as MarkDropped does.
func (r *GameInstances) MarkTemplateDropped(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_templates SET status = 'dropped', updated_at = now() WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("mark the template of contest %s dropped: %w", contestID, err)
	}
	return nil
}
