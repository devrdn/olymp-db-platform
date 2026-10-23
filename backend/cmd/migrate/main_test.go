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

// scratchDatabase creates an empty _test database next to CORE_DB_DSN's and
// drops it afterwards. Rolling back would drop tables other packages are
// using in parallel on the shared test database.
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

// monitoringSchema is which of the monitoring objects exist in a database.
type monitoringSchema struct {
	events, revisions, ip, fingerprint, eventIndexes, failedLoginIndex, keysetIndexes bool
	permission                                                                        bool
	grants                                                                            int
	// mismatched counts roles holding exactly one of contest.view and
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
		       -- No index on query_log's fingerprint: its one reader reads a
		       -- registration's whole range, and every console insert would pay
		       -- for it.
		       to_regclass('query_log_registration_fingerprint_idx') IS NULL
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
		Scan(&s.events, &s.revisions, &s.ip, &s.fingerprint, &s.eventIndexes, &s.failedLoginIndex, &s.keysetIndexes, &s.permission, &s.grants, &s.mismatched)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	return s
}

// TestTheMonitoringMigrationRollsBackAndForward proves, through the
// deployment's migrator, that the down file undoes exactly the up file and
// the up file re-applies.
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
	if down.events || down.revisions || down.ip || down.fingerprint || down.eventIndexes || down.failedLoginIndex || down.keysetIndexes || down.permission || down.grants != 0 {
		t.Fatalf("after rolling back 000033 something is left: %+v", down)
	}

	if err := m.Migrate(33); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 33: %v", err)
	}
	up := readMonitoringSchema(t, dsn)
	if !up.events || !up.revisions || !up.ip || !up.fingerprint || !up.eventIndexes || !up.failedLoginIndex || !up.keysetIndexes || !up.permission {
		t.Fatalf("after applying 000033 something is missing: %+v", up)
	}
	// contest.monitor is held by exactly the roles that hold contest.view.
	if up.grants == 0 || up.mismatched != 0 {
		t.Fatalf("contest.monitor is granted to %d roles, %d roles hold only one of it and contest.view; "+
			"want the same roles as contest.view, and not none", up.grants, up.mismatched)
	}
}

func TestTheMigrationConnectionDoesNotCutAMigrationShort(t *testing.T) {
	cfg, err := migrationConfig("postgres://u:p@localhost:5432/db?sslmode=disable")
	if err != nil {
		t.Fatalf("migrationConfig() = %v", err)
	}

	if got := cfg.RuntimeParams["statement_timeout"]; got != "0" {
		t.Errorf("statement_timeout = %q, want 0: an index build is long by nature", got)
	}
	if got, set := cfg.RuntimeParams["lock_timeout"]; set {
		t.Errorf("lock_timeout = %q on the connection: it belongs to the migration that wants it", got)
	}
}

// questionTitleColumn is how question_translations.title stands in a
// database: absent, or present with its default and its bound.
type questionTitleColumn struct {
	exists, notNull, bounded bool
	defaultExpr              string
}

func readQuestionTitleColumn(t *testing.T, dsn string) questionTitleColumn {
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

	var c questionTitleColumn
	err = conn.QueryRow(ctx, `
		SELECT count(*) > 0,
		       COALESCE(bool_and(is_nullable = 'NO'), false),
		       COALESCE(max(column_default), ''),
		       EXISTS (SELECT 1 FROM pg_constraint
		               WHERE conrelid = 'question_translations'::regclass AND contype = 'c'
		                 AND pg_get_constraintdef(oid) LIKE '%char_length(title) <= 200%')
		FROM information_schema.columns
		WHERE table_name = 'question_translations' AND column_name = 'title'`).
		Scan(&c.exists, &c.notNull, &c.defaultExpr, &c.bounded)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	return c
}

// An existing question keeps working: its translations gain an empty title,
// which the play screen reads as "no title".
func TestTheQuestionTitleMigrationRollsBackAndForward(t *testing.T) {
	dsn := scratchDatabase(t)

	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	defer closeFn()

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := m.Migrate(44); err != nil {
		t.Fatalf("migrate to 44: %v", err)
	}
	if down := readQuestionTitleColumn(t, dsn); down.exists || down.bounded {
		t.Fatalf("after rolling back 000045 the title is left: %+v", down)
	}

	if err := m.Migrate(45); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 45: %v", err)
	}
	up := readQuestionTitleColumn(t, dsn)
	if !up.exists || !up.notNull || up.defaultExpr != "''::text" || !up.bounded {
		t.Fatalf("after applying 000045 title = %+v, want NOT NULL DEFAULT '' bounded at 200 characters", up)
	}
}

// schemaNotesTable is what of the table descriptions' table exists.
type schemaNotesTable struct {
	exists, bounded, cascades bool
}

func readSchemaNotesTable(t *testing.T, dsn string) schemaNotesTable {
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

	var s schemaNotesTable
	err = conn.QueryRow(ctx, `
		SELECT to_regclass('contest_schema_notes') IS NOT NULL,
		       EXISTS (SELECT 1 FROM pg_constraint
		               WHERE conname = 'contest_schema_notes_body_length'
		                 AND pg_get_constraintdef(oid) LIKE '%char_length(body_md) <= 100000%'),
		       EXISTS (SELECT 1 FROM pg_constraint
		               WHERE conname = 'contest_schema_notes_contest_id_fkey' AND confdeltype = 'c')`).
		Scan(&s.exists, &s.bounded, &s.cascades)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	return s
}

func TestTheSchemaNotesMigrationRollsBackAndForward(t *testing.T) {
	dsn := scratchDatabase(t)

	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	defer closeFn()

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := m.Migrate(45); err != nil {
		t.Fatalf("migrate to 45: %v", err)
	}
	if down := readSchemaNotesTable(t, dsn); down.exists || down.bounded || down.cascades {
		t.Fatalf("after rolling back 000046 the table is left: %+v", down)
	}

	if err := m.Migrate(46); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 46: %v", err)
	}
	if up := readSchemaNotesTable(t, dsn); !up.exists || !up.bounded || !up.cascades {
		t.Fatalf("after applying 000046 = %+v, want the table, bounded at 100000 characters, deleted with its contest", up)
	}
}

// prizeColumns counts which of the three prize columns exist, and how many of
// them are NOT NULL DEFAULT 0 with a CHECK bounding them at 0..1000.
type prizeColumns struct {
	present, shaped int
}

func readPrizeColumns(t *testing.T, dsn string) prizeColumns {
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

	var p prizeColumns
	err = conn.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE c.is_nullable = 'NO' AND c.column_default = '0'
		                          AND c.data_type = 'integer'
		                          AND EXISTS (SELECT 1 FROM pg_constraint k
		                                      WHERE k.conrelid = 'contests'::regclass
		                                        AND k.conname = 'contests_' || c.column_name || '_range'
		                                        AND pg_get_constraintdef(k.oid) LIKE '%>= 0%'
		                                        AND pg_get_constraintdef(k.oid) LIKE '%<= 1000%'))
		FROM information_schema.columns c
		WHERE c.table_name = 'contests'
		  AND c.column_name IN ('prize_first', 'prize_second', 'prize_third')`).
		Scan(&p.present, &p.shaped)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	return p
}

func TestThePrizePlacesMigrationRollsBackAndForward(t *testing.T) {
	dsn := scratchDatabase(t)

	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	defer closeFn()

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := m.Migrate(46); err != nil {
		t.Fatalf("migrate to 46: %v", err)
	}
	if down := readPrizeColumns(t, dsn); down.present != 0 {
		t.Fatalf("after rolling back 000047 the prize columns are left: %+v", down)
	}

	if err := m.Migrate(47); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 47: %v", err)
	}
	if up := readPrizeColumns(t, dsn); up.present != 3 || up.shaped != 3 {
		t.Fatalf("after applying 000047 = %+v, want three integer columns NOT NULL DEFAULT 0 bounded at 0..1000", up)
	}
}
