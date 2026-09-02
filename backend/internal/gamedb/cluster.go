// Package gamedb prepares the cluster that participants' queries run against.
//
// It answers one question: what can a participant's database role do, and to
// what. That is the layer the architecture calls the main security boundary
// (section 5, point 4) — the one that has to hold even if the SQL validator is
// bypassed entirely, because it is the only one that is not made of the
// application's own code.
//
// What it deliberately does not do: execute participants' queries, decide how
// many may run at once, or create the per-participant databases. Execution and
// admission control belong to the Query Runner; provisioning is built on top
// of the primitives here.
//
// # What the layers below actually guarantee
//
// Worth stating plainly, because the difference is not visible in the SQL and
// was measured rather than assumed:
//
//   - Privileges are a real boundary. A role with no INSERT cannot insert, and
//     nothing the session does changes that. This is what stops every write.
//   - Catalog REVOKEs are a real boundary, and they survive CREATE DATABASE …
//     TEMPLATE, which is how they reach a participant at all.
//   - temp_file_limit is a real boundary: it is not USERSET, so a session
//     cannot raise it.
//   - statement_timeout and default_transaction_read_only are NOT boundaries.
//     Both are USERSET, and `SET` is itself SQL: a session can turn either off
//     in one statement. They are defaults that bound the ordinary case. What
//     bounds time against a query that reached the database unchecked is the
//     Query Runner's own deadline and cancellation, which no SQL can reach.
//
// The tests in this package provoke each of those, including the last one, so
// that the distinction stays written down rather than remembered.
package gamedb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The two roles the Query Runner connects as, and the only ones that ever
// touch a participant's database. Neither is handed to a participant: a
// participant reaches this cluster through the interface and nowhere else.
const (
	RoleReader = "game_reader"
	RoleWriter = "game_writer"
)

// connectionLimit is the last line under the Query Runner's semaphore.
//
// Reaching it means admission control has already failed, so it is set high
// enough never to be the thing that shapes normal load and low enough that a
// broken runner cannot exhaust the cluster's connections.
const connectionLimit = 60

// Roles carries the credentials the two participant roles are given.
type Roles struct {
	ReaderPassword string
	WriterPassword string
}

// Conn is the part of a connection this package uses.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sessionDefaults are applied to both roles with ALTER ROLE … SET.
//
// Defaults, not limits — see the package comment. They are still worth setting:
// they bound every query that arrives the ordinary way, which is all of them
// unless something else has already gone wrong.
var sessionDefaults = [][2]string{
	{"statement_timeout", "5s"},
	{"idle_in_transaction_session_timeout", "5s"},
	{"work_mem", "16MB"},
	{"temp_file_limit", "64MB"},
	{"lock_timeout", "2s"},
}

// PrepareCluster makes the cluster ready to host participant databases.
//
// Idempotent, because it runs on every deploy and an already-prepared cluster
// is the normal case. It is also the only place the roles are defined, so
// running it after an upgrade is how a new restriction reaches a cluster that
// already exists — which is why it is a program rather than an init script
// that the image runs once and silently skips ever after.
func PrepareCluster(ctx context.Context, conn Conn, roles Roles) error {
	for _, role := range []struct {
		name     string
		password string
		readOnly bool
	}{
		{RoleReader, roles.ReaderPassword, true},
		{RoleWriter, roles.WriterPassword, false},
	} {
		if err := prepareRole(ctx, conn, role.name, role.password, role.readOnly); err != nil {
			return fmt.Errorf("preparing %s: %w", role.name, err)
		}
	}
	return keepRolesOutOfMaintenanceDatabases(ctx, conn)
}

// prepareRole creates the role if it is missing and then states, in full, what
// it may be.
//
// The ALTER is unconditional and lists every attribute, including the negative
// ones. A role that was hand-edited on a running cluster — granted CREATEDB to
// get something working during a contest — is put back by the next deploy,
// which is only true if the statement names what the role must *not* have as
// well as what it must.
func prepareRole(ctx context.Context, conn Conn, name, password string, readOnly bool) error {
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return fmt.Errorf("looking for the role: %w", err)
	}
	if !exists {
		create, err := formatted(ctx, conn, `SELECT format('CREATE ROLE %I LOGIN', $1::text)`, name)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, create); err != nil {
			return fmt.Errorf("creating the role: %w", err)
		}
	}

	alter, err := formatted(ctx, conn,
		`SELECT format(
			'ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE '
			'NOREPLICATION NOBYPASSRLS CONNECTION LIMIT %s PASSWORD %L',
			$1::text, $2::int, $3::text)`,
		name, connectionLimit, password)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, alter); err != nil {
		return fmt.Errorf("setting the role's attributes: %w", err)
	}

	defaults := sessionDefaults
	if readOnly {
		defaults = append(append([][2]string{}, defaults...),
			[2]string{"default_transaction_read_only", "on"})
	}
	for _, setting := range defaults {
		// The setting's name is a constant of this package, never input, so it
		// goes in as text; only the value is quoted by the server.
		statement, err := formatted(ctx, conn,
			`SELECT format('ALTER ROLE %I SET `+setting[0]+` = %L', $1::text, $2::text)`, name, setting[1])
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("setting %s: %w", setting[0], err)
		}
	}
	return nil
}

// keepRolesOutOfMaintenanceDatabases stops a participant's role connecting to
// anything that is not a game.
//
// It does not separate one participant from another — they share a role, and
// what keeps them apart is that the Query Runner takes the database name from
// game_instances and never from the client. What it does is keep the role out
// of the cluster's own databases and out of the one the provisioner works in.
func keepRolesOutOfMaintenanceDatabases(ctx context.Context, conn Conn) error {
	var maintenance string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&maintenance); err != nil {
		return fmt.Errorf("asking which database this is: %w", err)
	}

	for _, database := range []string{"postgres", "template1", maintenance} {
		statement, err := formatted(ctx, conn,
			`SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', $1::text)`, database)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("revoking connect on %s: %w", database, err)
		}
	}
	return nil
}

// formatted asks the server to build a statement, so that identifiers and
// literals are quoted by PostgreSQL's own rules.
//
// DDL cannot take bound parameters — a role name and a password have to end up
// inside the statement text. Hand-rolled quoting is the classic place to get
// that subtly wrong, and wrong here means a password containing a quote either
// breaks the deploy or, worse, ends the literal early. format's %I and %L are
// the same code PostgreSQL uses for its own dumps.
func formatted(ctx context.Context, conn Conn, query string, args ...any) (string, error) {
	var statement string
	if err := conn.QueryRow(ctx, query, args...).Scan(&statement); err != nil {
		return "", fmt.Errorf("building the statement: %w", err)
	}
	return statement, nil
}
