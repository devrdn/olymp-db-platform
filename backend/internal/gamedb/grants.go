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
		`CREATE SCHEMA IF NOT EXISTS ` + sqlpolicy.QuoteIdentifier(workSchema),
		`GRANT USAGE ON SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
		`GRANT USAGE ON SCHEMA ` + sqlpolicy.QuoteIdentifier(workSchema) + ` TO ` + RoleReader + `, ` + RoleWriter,
		// Reading the game is what every contest permits; the modes differ in
		// what else they allow.
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + RoleReader + `, ` + RoleWriter,
	}

	if policy.Mode == sqlpolicy.ModeReadWrite {
		if policy.AllowOwnTables || policy.AllowCreateView {
			statements = append(statements,
				`GRANT CREATE ON SCHEMA `+sqlpolicy.QuoteIdentifier(workSchema)+` TO `+RoleWriter)
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

// lendTemplateToTheAuthor gives the game-script role exactly what building a
// game needs, inside one template database and nowhere else.
//
// Grants rather than ownership, deliberately. Making game_author the owner of
// the template would be the obvious way to say "this is yours", and it hands
// back most of what this boundary is for: a database owner may DROP DATABASE,
// the check is ownership alone, and one role owning every contest's template
// means a script in one olympiad's template can drop another olympiad's — with
// no connection to it, from the session it is already in. (Only as a single
// statement: a multi-statement simple query runs inside an implicit
// transaction and DROP DATABASE refuses in one. A script is whatever text an
// organiser uploaded, so that is not a constraint on the attacker.) With
// grants, the provisioning role stays the owner and there is no database
// anywhere on the cluster that game_author may drop, rename or reassign.
//
// It must run connected to the database being lent.
func lendTemplateToTheAuthor(ctx context.Context, conn Conn, database string) error {
	return runAll(ctx, conn, []string{
		// CREATE on the database is what lets a script say CREATE SCHEMA, and
		// is also the privilege a trusted extension (citext, pgcrypto) is
		// checked against — both things a real game script does. It is not
		// ownership: it cannot drop or rename the database.
		`GRANT CREATE, CONNECT, TEMPORARY ON DATABASE ` + sqlpolicy.QuoteIdentifier(database) + ` TO ` + RoleAuthor,
		// The game's tables live in public and the author has to own them: a
		// view or a SECURITY DEFINER function executes as its owner, so a game
		// whose objects belonged to somebody else would either not work or
		// would work with somebody else's privileges.
		`GRANT USAGE, CREATE ON SCHEMA public TO ` + RoleAuthor,
	})
}

// takeTheTemplateBackFromTheAuthor withdraws all of it again, before the
// template is copied.
//
// The two REVOKEs travel differently, which is the whole reason both are here.
// The schema ACL lives in the template's own catalogue and is copied by CREATE
// DATABASE … TEMPLATE, so without the first one every participant's database
// would ship with a role that may create objects in the game's own schema. The
// database ACL lives on the pg_database row and is *not* copied, so the second
// one closes this template rather than the copies — a rebuild is a fresh
// CREATE DATABASE and lends the privileges again.
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

// instanceConnectionLimit is the last line under the Query Runner's semaphore.
//
// The runner never holds more than one connection per database: a participant
// is allowed one query at a time, and it keeps at most one idle connection per
// database, which the next query takes rather than opening another. The
// schema panel's catalogue read (ReadSchema) does not count against the limit
// at all: it connects as the provisioning role, a superuser, and PostgreSQL
// does not apply a database's CONNECTION LIMIT to superusers.
//
// Two rather than one is headroom for the runner's own turnover: a connection
// it has just closed is still counted until its server backend has exited,
// which happens after the close returns, and a limit of one could refuse the
// next query that opens a connection in that moment.
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
	name := sqlpolicy.QuoteIdentifier(instance)

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
		return "public." + sqlpolicy.QuoteIdentifier(table)
	}
	return sqlpolicy.QuoteIdentifier(schema) + "." + sqlpolicy.QuoteIdentifier(name)
}
