package gamedb

import (
	"context"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// workSchema is where a participant's own objects live. The game's tables sit
// in `public`, where a participant never has CREATE, so creating objects and
// changing the contest are separate permissions.
const workSchema = "work"

// grantPolicy turns the contest's policy into privileges inside the template,
// so a statement the validator wrongly admits is still impossible. Both layers
// are built from the same Policy. Database-level grants are not copied with
// the template, so temporary tables are settled per instance.
func grantPolicy(ctx context.Context, conn Conn, policy sqlpolicy.Policy) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS ` + sqlpolicy.QuoteIdentifier(workSchema),
		`GRANT USAGE ON SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
		`GRANT USAGE ON SCHEMA ` + sqlpolicy.QuoteIdentifier(workSchema) + ` TO ` + RoleReader + `, ` + RoleWriter,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
	}

	if policy.Mode == sqlpolicy.ModeReadWrite {
		if policy.AllowOwnTables || policy.AllowCreateView {
			statements = append(statements,
				`GRANT CREATE ON SCHEMA `+sqlpolicy.QuoteIdentifier(workSchema)+` TO `+RoleWriter)
		}
		statements = append(statements, writableTableGrants(policy)...)
	}

	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply the policy (%s): %w", statement, err)
		}
	}
	return nil
}

// writableTableGrants is what the writer role gets on the policy's writable
// tables. Both grantPolicy (template) and settleInstance (each copy) apply it,
// so the two paths cannot diverge. Empty when nothing is writable.
func writableTableGrants(policy sqlpolicy.Policy) []string {
	if len(policy.WritableTables) == 0 {
		return nil
	}

	statements := make([]string, 0, len(policy.WritableTables)+1)
	for _, table := range policy.WritableTables {
		// The policy has already refused names that are not plain identifiers.
		// TRUNCATE matches what the validator permits on these tables; it adds
		// nothing DELETE cannot do, and it is how a participant at the disk
		// quota frees pages.
		statements = append(statements,
			`GRANT INSERT, UPDATE, DELETE, TRUNCATE ON `+qualify(table)+` TO `+RoleWriter)
	}
	// Inserting into a serial column needs its sequence, and the policy does
	// not name sequences.
	return append(statements,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO `+RoleWriter)
}

// lendTemplateToTheAuthor gives the game-script role what building a game
// needs, inside one template database. It must run connected to that database.
//
// Grants, not ownership: a database owner may DROP DATABASE, so one role owning
// every template would let a script in one contest drop another's. The
// provisioning role stays the owner.
func lendTemplateToTheAuthor(ctx context.Context, conn Conn, database string) error {
	return runAll(ctx, conn, []string{
		// CREATE allows CREATE SCHEMA and trusted extensions (citext, pgcrypto),
		// but not dropping or renaming the database.
		`GRANT CREATE, CONNECT, TEMPORARY ON DATABASE ` + sqlpolicy.QuoteIdentifier(database) + ` TO ` + RoleAuthor,
		// The author must own the game's objects: views and SECURITY DEFINER
		// functions execute as their owner.
		`GRANT USAGE, CREATE ON SCHEMA public TO ` + RoleAuthor,
	})
}

// takeTheTemplateBackFromTheAuthor withdraws both grants before the template
// is copied. The schema ACL is copied by CREATE DATABASE … TEMPLATE, so without
// its REVOKE every instance would let the author create objects in `public`.
// The database ACL is not copied, so its REVOKE closes only this template.
func takeTheTemplateBackFromTheAuthor(ctx context.Context, conn Conn, database string) error {
	return runAll(ctx, conn, []string{
		`REVOKE ALL ON SCHEMA public FROM ` + RoleAuthor,
		`REVOKE ALL ON DATABASE ` + sqlpolicy.QuoteIdentifier(database) + ` FROM ` + RoleAuthor,
	})
}

// runAll executes statements in order, naming the one that failed.
func runAll(ctx context.Context, conn Conn, statements []string) error {
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

// instanceConnectionLimit is a backstop under the Query Runner's semaphore,
// which holds at most one connection per database. Two leaves room for a
// closed connection whose backend has not exited yet. ReadSchema connects as a
// superuser, to whom CONNECTION LIMIT does not apply.
const instanceConnectionLimit = 2

// settleInstance applies, on one participant's database, what a copy does not
// get from its template.
//
//   - Database-level privileges: the ACL lives on the pg_database row and is
//     not copied, and PUBLIC has TEMPORARY by default, so
//     `allow_temp_tables: false` would do nothing if set only in the template.
//   - Writable-table grants: a copy holds them as of the template's last
//     build, so re-granting makes them follow the running policy. GRANT is
//     idempotent.
//
// Table grants need a connection to the instance itself, opened only when the
// policy has writable tables.
func (p *Provisioner) settleInstance(ctx context.Context, instance string, policy sqlpolicy.Policy) error {
	name := sqlpolicy.QuoteIdentifier(instance)

	if _, err := p.admin.Exec(ctx, fmt.Sprintf(
		`ALTER DATABASE %s CONNECTION LIMIT %d`, name, instanceConnectionLimit)); err != nil {
		return fmt.Errorf("limit connections to %s: %w", instance, err)
	}

	if _, err := p.admin.Exec(ctx, `REVOKE TEMPORARY ON DATABASE `+name+` FROM PUBLIC`); err != nil {
		return fmt.Errorf("revoke temporary tables on %s: %w", instance, err)
	}
	if policy.Mode == sqlpolicy.ModeReadWrite && policy.AllowTempTables {
		if _, err := p.admin.Exec(ctx, `GRANT TEMPORARY ON DATABASE `+name+` TO `+RoleWriter); err != nil {
			return fmt.Errorf("grant temporary tables on %s: %w", instance, err)
		}
	}

	if policy.Mode != sqlpolicy.ModeReadWrite {
		return nil
	}
	statements := writableTableGrants(policy)
	if len(statements) == 0 {
		return nil
	}

	// The provisioning role is a superuser, so the limit above does not apply.
	conn, err := p.connect(ctx, p.base.User, instance)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply the policy to %s (%s): %w", instance, statement, err)
		}
	}
	return nil
}

// qualify quotes schema and table separately; quoting `public.evidence` whole
// names a different table. The policy refuses names with more than two parts.
func qualify(table string) string {
	schema, name, ok := strings.Cut(table, ".")
	if !ok {
		return "public." + sqlpolicy.QuoteIdentifier(table)
	}
	return sqlpolicy.QuoteIdentifier(schema) + "." + sqlpolicy.QuoteIdentifier(name)
}
