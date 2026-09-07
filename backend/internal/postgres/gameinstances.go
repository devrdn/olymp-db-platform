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

// GameInstances is the record of which database belongs to whom.
//
// A spare copy and a participant's instance are one row apart: the spare has
// no registration. That is what lets a copy be claimed with a single statement
// instead of a delete and an insert, which matters when two late registrations
// reach for the last one at the same moment.
type GameInstances struct{ pool *pgxpool.Pool }

var _ provisioning.Repository = (*GameInstances)(nil)

// NewGameInstances returns a repository over pool.
func NewGameInstances(pool *pgxpool.Pool) *GameInstances { return &GameInstances{pool: pool} }

func (r *GameInstances) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ClaimSpare hands one free copy to a registration, or reports there is none.
//
// `FOR UPDATE SKIP LOCKED` is the whole trick. Two claimants arriving together
// do not queue behind one row and then find it taken — the second skips past
// it to the next free copy, so N claimants take N different databases in one
// round trip each. Without SKIP LOCKED the second would block, wake, and have
// to discover its row was claimed; without the lock at all, both would take
// the same one.
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

// Assign records a database created for one participant directly, which is
// what happens when the pool was empty.
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

// Stale lists the databases of a contest that came from an older template.
//
// Both the claimed and the free ones: a participant must not play on old data
// or old grants, and a spare copy of the old version is a trap waiting for the
// next person to register.
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

// SpareCount is how deep the pool is for one version of a contest's template.
// It is a metric with an alert on it (section 9), and the number the top-up
// works towards.
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

// AllCurrent reports whether every instance of a contest came from the current
// template. Section 4.2 makes it a precondition for starting.
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

// Game returns one contest's game: which template it plays on, at what version,
// under which policy.
//
// The same shape Live returns, for the one contest a participant is asking
// about. A template that is still building or has failed is no game yet, which
// is a different answer from "no such contest" and reads differently to the
// person waiting.
func (r *GameInstances) Game(ctx context.Context, contestID uuid.UUID) (provisioning.Contest, error) {
	var c provisioning.Contest
	var mode string
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT c.id, t.template_db, t.version,
		       coalesce(p.mode, 'read_only'),
		       coalesce(p.writable_tables, '{}')::text[],
		       coalesce(p.allow_create_view, false),
		       coalesce(p.allow_own_tables, false),
		       coalesce(p.allow_temp_tables, false),
		       coalesce(p.allow_catalog, true),
		       coalesce(p.disk_quota_ratio, 5)
		FROM contests c
		JOIN game_templates t ON t.contest_id = c.id
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE c.id = $1 AND t.status = 'ready'`, contestID).
		Scan(&c.ID, &c.Template, &c.Version,
			&mode, &c.Policy.WritableTables, &c.Policy.AllowCreateView,
			&c.Policy.AllowOwnTables, &c.Policy.AllowTempTables, &c.Policy.AllowCatalog,
			&c.Policy.DiskQuotaRatio)

	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Contest{}, provisioning.ErrNoGame
	}
	if err != nil {
		return provisioning.Contest{}, fmt.Errorf("read the contest's game: %w", err)
	}
	c.Policy.Mode = sqlpolicy.Mode(mode)
	return c, nil
}

// Live lists the contests whose pool is worth keeping stocked.
//
// Published or running, and only with a template that finished building: a
// draft has nobody to provision for, and a template still building or failed
// would have copies made of a database that is not a contest.
func (r *GameInstances) Live(ctx context.Context) ([]provisioning.Contest, error) {
	// The policy is joined from contest_sql_policies, which has held it since
	// the schema's second migration. A contest that was never configured has
	// no row there, and LEFT JOIN plus coalesce gives it the read-only
	// default — the absence of a policy must never read as no restrictions.
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT c.id, t.template_db, t.version,
		       coalesce(p.mode, 'read_only'),
		       coalesce(p.writable_tables, '{}')::text[],
		       coalesce(p.allow_create_view, false),
		       coalesce(p.allow_own_tables, false),
		       coalesce(p.allow_temp_tables, false),
		       coalesce(p.allow_catalog, true),
		       coalesce(p.disk_quota_ratio, 5)
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
		if err := rows.Scan(&c.ID, &c.Template, &c.Version,
			&mode, &c.Policy.WritableTables, &c.Policy.AllowCreateView,
			&c.Policy.AllowOwnTables, &c.Policy.AllowTempTables, &c.Policy.AllowCatalog, &c.Policy.DiskQuotaRatio); err != nil {
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

// reclaimDeadline is the SQL fragment both Reclaimable and ReclaimableTemplates
// share: the moment a contest's own grace period runs out, computed from its
// finish (or archiving) moment rather than from a value this process
// computes. SetStatus and AdvanceFinished (contests.go) both set updated_at
// in the same statement that moves status to finished, and archiving a
// contest is itself an update of the same row, so updated_at is later still
// — the same clock DueToStart and AdvanceFinished already trust for "has
// enough time passed" (their own reasoning about replica clock skew applies
// here too — now() is this database's own).
//
// A contest's settings.grace_period_min governs it when set; a contest that
// never configured one (the JSON key absent, coalesce's 0) defers to
// installationGraceMin — provisioning.Service.Reclaim's own caller supplies
// the installation's configured default, the same "contest overrides,
// installation is the fallback" convention queryproxy.effectiveRateLimit
// documents for QueryRateLimitPerMin. $1 is installationGraceMin in both
// callers below.
const reclaimDeadline = `c.updated_at + make_interval(mins => CASE
	        WHEN coalesce((c.settings->>'grace_period_min')::int, 0) > 0
	          THEN (c.settings->>'grace_period_min')::int
	        ELSE $1
	      END)`

// Reclaimable lists up to limit not-yet-dropped instances of a contest that
// reached 'finished' or 'archived' longer ago than its grace period allows.
//
// Both statuses, not only 'finished': contests.allowedTransitions lets an
// organizer move a finished contest straight to 'archived', and archiving is
// the ordinary "put this away" action, not a reason to exempt a contest's
// instances from the sweep forever — the leak this whole pass exists to
// close. updated_at at the moment of archiving is later than the finish
// moment reclaimDeadline is built from either way, so the same grace still
// protects a contest that was archived the instant it finished exactly as it
// would have unarchived.
//
// limit is what keeps a fresh deployment's first tick from issuing one DROP
// DATABASE per contest that finished before this sweep existed — see
// provisioning.ReclaimBatchLimit's own doc for the number and why. The
// ordering — by deadline, oldest debt first, contest_id and created_at only
// to break an exact tie deterministically — is what keeps that limit fair
// across contests: contest_id is a UUID, so ordering by it first would let
// whichever contest happens to sort first monopolise every tick's whole
// batch until it drains, leaving every other contest's overdue instances
// waiting behind it for hours. Ordering by deadline instead means the limit
// is spent on whoever has been owed a drop the longest, not on an accident
// of UUID generation.
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

// MarkDropped moves one instance to the terminal 'dropped' status. An update
// rather than Forget's delete: the row is what an organizer's audit search
// still has to point to once the database itself is gone from the cluster.
func (r *GameInstances) MarkDropped(ctx context.Context, database string) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_instances SET status = 'dropped', updated_at = now() WHERE db_name = $1`, database); err != nil {
		return fmt.Errorf("mark %s dropped: %w", database, err)
	}
	return nil
}

// ReclaimableTemplates lists up to limit contest templates that may be
// dropped: the contest reached 'finished' or 'archived' longer ago than its
// grace period, its template is a built database ('ready' — a template still
// 'pending', 'building' or already 'failed' has nothing on disk this could
// remove), and every instance ever copied from it is already gone.
//
// That last condition — the NOT EXISTS below — is what makes the ordering
// against Reclaimable safe: a template is never offered while a live
// instance could still need the copy it was made from, so
// provisioning.Service.Reclaim calling this after it has finished dropping
// instances is what lets a contest whose very last instance was just
// dropped this same tick have its template reclaimed in the same tick too,
// rather than waiting for a tick where the NOT EXISTS has already gone stale
// in this query's favour.
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

// MarkTemplateDropped moves one contest's template to the terminal 'dropped'
// status — MarkDropped's own convention, kept for the same reason: the row
// is what an organizer's audit search still has to point to once the
// database itself is gone.
func (r *GameInstances) MarkTemplateDropped(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_templates SET status = 'dropped', updated_at = now() WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("mark the template of contest %s dropped: %w", contestID, err)
	}
	return nil
}
