package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
)

// scratchDatabase creates an empty database of its own next to the one
// CORE_DB_DSN names, and returns a DSN for it.
//
// Its own database rather than the shared test database: rolling a migration
// back drops tables other test packages are using at the same moment, and
// `go test` runs packages in parallel. The scratch name ends in the test
// suffix like every database the tests touch, and it is dropped afterwards.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(storagetest.CoreDSNVar)
	if dsn == "" {
		t.Skip("set CORE_DB_DSN to run the migration round trip")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("%s is not a valid connection string", storagetest.CoreDSNVar)
	}
	if err := storagetest.CheckName(cfg.Database); err != nil {
		t.Fatal(err)
	}
	scratch := strings.TrimSuffix(cfg.Database, storagetest.Suffix) + "_migrations" + storagetest.Suffix
	if err := storagetest.CheckName(scratch); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	maintenance := cfg.Copy()
	maintenance.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, maintenance)
	if err != nil {
		t.Fatalf("connect to the maintenance database: %v", err)
	}
	quoted := pgx.Identifier{scratch}.Sanitize()
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
		t.Fatalf("drop %s: %v", scratch, err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoted); err != nil {
		t.Fatalf("create %s: %v", scratch, err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = conn.Exec(clean, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`)
		_ = conn.Close(clean)
	})

	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Skip("the migration round trip needs CORE_DB_DSN in URL form")
	}
	parsed.Path = "/" + scratch
	return parsed.String()
}

// monitoringSchema is which of the monitoring objects exist in a database:
// the two tables, the two query_log columns, their index, and the
// contest.monitor permission with its grants.
type monitoringSchema struct {
	events, revisions, ip, fingerprint, fingerprintIndex, failedLoginIndex, keysetIndexes bool
	permission                                                                            bool
	// grants is how many roles hold contest.monitor.
	grants int
	// mismatched is how many roles hold exactly one of contest.view and
	// contest.monitor.
	mismatched int
}

func readMonitoringSchema(t *testing.T, dsn string) monitoringSchema {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if err := storagetest.Guard(ctx, conn); err != nil {
		t.Fatal(err)
	}

	var s monitoringSchema
	err = conn.QueryRow(ctx, `
		SELECT to_regclass('participant_events') IS NOT NULL,
		       to_regclass('workspace_revisions') IS NOT NULL,
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_name = 'query_log' AND column_name = 'ip' AND data_type = 'inet'),
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_name = 'query_log' AND column_name = 'sql_fingerprint' AND data_type = 'bigint'),
		       to_regclass('query_log_registration_fingerprint_idx') IS NOT NULL
		       AND to_regclass('participant_events_contest_time_idx') IS NOT NULL
		       AND to_regclass('participant_events_registration_time_idx') IS NOT NULL
		       AND to_regclass('participant_events_contest_idx') IS NULL,
		       to_regclass('audit_log_failed_login_idx') IS NOT NULL,
		       pg_get_indexdef('query_log_registration_executed_idx'::regclass) LIKE '%(registration_id, executed_at, id)%'
		       AND pg_get_indexdef('submissions_registration_submitted_idx'::regclass) LIKE '%(registration_id, submitted_at, id)%',
		       EXISTS (SELECT 1 FROM permissions WHERE code = 'contest.monitor'),
		       (SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		        WHERE p.code = 'contest.monitor'),
		       (SELECT count(*) FROM (
		            (SELECT rp.role_id FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		             WHERE p.code = 'contest.view'
		             EXCEPT
		             SELECT rp.role_id FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		             WHERE p.code = 'contest.monitor')
		            UNION ALL
		            (SELECT rp.role_id FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		             WHERE p.code = 'contest.monitor'
		             EXCEPT
		             SELECT rp.role_id FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		             WHERE p.code = 'contest.view')) AS differ)`).
		Scan(&s.events, &s.revisions, &s.ip, &s.fingerprint, &s.fingerprintIndex, &s.failedLoginIndex, &s.keysetIndexes, &s.permission, &s.grants, &s.mismatched)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	return s
}

// TestTheMonitoringMigrationRollsBackAndForward applies every migration to an
// empty database, rolls the participant-monitoring one back and applies it
// again, through the same migrator the deployment runs — so the down file is
// proven to undo exactly what the up file did, and the up file to be
// re-appliable after it.
func TestTheMonitoringMigrationRollsBackAndForward(t *testing.T) {
	dsn := scratchDatabase(t)

	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	defer closeFn()

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if version, _, _ := m.Version(); version < 33 {
		t.Fatalf("version after up = %d, want at least 33", version)
	}
	// Roll back to just before 000033, whatever came after it.
	if err := m.Migrate(32); err != nil {
		t.Fatalf("migrate to 32: %v", err)
	}
	down := readMonitoringSchema(t, dsn)
	if down.events || down.revisions || down.ip || down.fingerprint || down.fingerprintIndex || down.failedLoginIndex || down.keysetIndexes || down.permission || down.grants != 0 {
		t.Fatalf("after rolling back 000033 something is left: %+v", down)
	}

	if err := m.Migrate(33); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 33: %v", err)
	}
	up := readMonitoringSchema(t, dsn)
	if !up.events || !up.revisions || !up.ip || !up.fingerprint || !up.fingerprintIndex || !up.failedLoginIndex || !up.keysetIndexes || !up.permission {
		t.Fatalf("after applying 000033 something is missing: %+v", up)
	}
	// contest.monitor is held by exactly the roles that hold contest.view.
	if up.grants == 0 || up.mismatched != 0 {
		t.Fatalf("contest.monitor is granted to %d roles, %d roles hold only one of it and contest.view; "+
			"want the same roles as contest.view, and not none", up.grants, up.mismatched)
	}
}
