package gamedb

import (
	"context"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// workSchema is where a participant's own objects live.
//
// Separate from the game's schema so that "may create things" and "may change
// the contest" are different permissions. The game's tables sit in `public`,
// which a participant never has CREATE on, so nothing they do can alter the
// shape of the contest.
const workSchema = "work"

// grantPolicy turns the contest's policy into privileges, inside the template.
//
// The other half of the layered defence: the validator refuses statements the
// policy does not permit, and these make the same statements impossible if the
// validator ever lets one through. Both are built from the one Policy, which
// is what stops them meaning different things.
//
// Everything here travels with the template, because CREATE DATABASE copies a
// database's catalogue. What does *not* travel is anything granted on the
// database itself — those live on the pg_database row and a copy gets a fresh
// one — which is why temporary tables are settled per instance instead.
func grantPolicy(ctx context.Context, conn Conn, policy sqlpolicy.Policy) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS ` + QuoteIdentifier(workSchema),
		`GRANT USAGE ON SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
		`GRANT USAGE ON SCHEMA ` + QuoteIdentifier(workSchema) + ` TO ` + RoleReader + `, ` + RoleWriter,
		// Reading the game is what every contest permits; the modes differ in
		// what else they allow.
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
	}

	if policy.Mode == sqlpolicy.ModeReadWrite {
		if policy.AllowOwnTables || policy.AllowCreateView {
			statements = append(statements,
				`GRANT CREATE ON SCHEMA `+QuoteIdentifier(workSchema)+` TO `+RoleWriter)
		}
		for _, table := range policy.WritableTables {
			// The policy has already refused any name that is not a plain
			// identifier, which is what makes this interpolation safe — there
			// is no way to bind an identifier, so the check has to happen
			// before the string is built.
			statements = append(statements,
				`GRANT INSERT, UPDATE, DELETE ON `+qualify(table)+` TO `+RoleWriter)
		}
		if len(policy.WritableTables) > 0 {
			// A table with a serial column cannot be inserted into without
			// its sequence. Granting per table would mean naming sequences the
			// author chose, which the policy does not describe.
			statements = append(statements,
				`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO `+RoleWriter)
		}
	}

	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply the policy (%s): %w", statement, err)
		}
	}
	return nil
}

// instanceConnectionLimit is the last line under the Query Runner's semaphore.
//
// Two rather than one: a query is running while the connection that will
// replace it is being opened, and a limit of one would turn an ordinary
// handover into a refusal. Reaching even two means admission control has
// already failed — a participant is allowed one query at a time, and the
// runner closes each connection when it is done.
const instanceConnectionLimit = 2

// settleInstance applies the privileges that belong to a database rather than
// to its contents.
//
// It is separate from grantPolicy and runs per instance because PostgreSQL
// grants TEMPORARY on a database to PUBLIC by default, and a database-level
// privilege is not copied from a template: the ACL belongs to the pg_database
// row, and a copy starts with a fresh one. Applied only in the template, the
// policy's `allow_temp_tables: false` would be a setting that quietly did
// nothing.
func settleInstance(ctx context.Context, conn Conn, instance string, policy sqlpolicy.Policy) error {
	name := QuoteIdentifier(instance)

	// Nothing should ever open a third connection to one participant's
	// database. This is not what enforces that — the runner's own semaphore
	// is — but it is what holds if the runner is wrong.
	if _, err := conn.Exec(ctx, fmt.Sprintf(
		`ALTER DATABASE %s CONNECTION LIMIT %d`, name, instanceConnectionLimit)); err != nil {
		return fmt.Errorf("limit connections to %s: %w", instance, err)
	}

	if _, err := conn.Exec(ctx, `REVOKE TEMPORARY ON DATABASE `+name+` FROM PUBLIC`); err != nil {
		return fmt.Errorf("revoke temporary tables on %s: %w", instance, err)
	}
	if policy.Mode == sqlpolicy.ModeReadWrite && policy.AllowTempTables {
		if _, err := conn.Exec(ctx, `GRANT TEMPORARY ON DATABASE `+name+` TO `+RoleWriter); err != nil {
			return fmt.Errorf("grant temporary tables on %s: %w", instance, err)
		}
	}
	return nil
}

// qualify spells a table the way a GRANT needs it.
//
// A name may carry its schema, because a contest's game schema need not be
// `public`. Quoting the whole thing would produce `"public.evidence"` — one
// identifier with a dot in it, which is a different table and almost certainly
// not one that exists. The policy has already refused anything with more parts
// than these, which is what makes splitting on the dot safe.
func qualify(table string) string {
	schema, name, ok := strings.Cut(table, ".")
	if !ok {
		return "public." + QuoteIdentifier(table)
	}
	return QuoteIdentifier(schema) + "." + QuoteIdentifier(name)
}
