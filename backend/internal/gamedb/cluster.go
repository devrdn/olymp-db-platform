// Package gamedb prepares the game cluster: what a participant's database role
// may do, and to what. This is the security boundary that must hold even if the
// SQL validator is bypassed. It does not execute participants' queries or do
// admission control; that is the Query Runner's job.
//
// # What actually holds
//
//   - Privileges and catalog REVOKEs are real boundaries, and both survive
//     CREATE DATABASE … TEMPLATE.
//   - temp_file_limit is a real boundary: it is not USERSET.
//   - statement_timeout and default_transaction_read_only are not: both are
//     USERSET, so a session can `SET` them off. They are defaults for the
//     ordinary case; the Query Runner's own deadline bounds time.
//
// The tests provoke each of these.
package gamedb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The two roles the Query Runner connects as. Neither is handed to a
// participant.
const (
	RoleReader = "game_reader"
	RoleWriter = "game_writer"
)

// RoleAuthor is who an organiser's game script runs as, and the only role the
// Core API authenticates as. Any single contest's manager may submit a script,
// while the cluster holds every contest's template, so the script must not run
// with the provisioner's privileges. It is a separate role with its own
// password and connection, because `SET ROLE` is undone by a `RESET ROLE` in
// the script.
const RoleAuthor = "game_author"

// connectionLimit is a backstop under the Query Runner's semaphore: high
// enough not to shape normal load, low enough that a broken runner cannot
// exhaust the cluster's connections.
const connectionLimit = 60

// authorConnectionLimit is how many game scripts may run at once. Each session
// runs arbitrary SQL and can allocate up to the per-process memory cap, so it
// is counted in the cluster's memory budget; config.Runner.MaxBuildSessions
// must equal it.
const authorConnectionLimit = 4

// Roles carries the credentials the three non-provisioning roles are given.
type Roles struct {
	ReaderPassword string
	WriterPassword string
	AuthorPassword string // the Core API's own credential, see RoleAuthor
}

// Conn is the part of a connection this package uses.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Cluster is a Conn that can also open a transaction and read many rows.
type Cluster interface {
	Conn
	Begin(ctx context.Context) (pgx.Tx, error)
	// Query reads only the cluster's own catalogue (Provisioner.DatabaseSizes),
	// never a participant's data.
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// prepareLock is the advisory lock every run of PrepareCluster takes; no other
// advisory lock may use this number. Two sessions altering the same role at
// once fail with `tuple concurrently updated` (XX000), so concurrent deploys
// must queue instead.
const prepareLock = 8_531_204_477_119_003_1

// sessionDefaults are applied to both participant roles with ALTER ROLE … SET.
// Most are defaults, not limits (see the package comment).
var sessionDefaults = [][2]string{
	{"statement_timeout", "5s"},
	{"idle_in_transaction_session_timeout", "5s"},
	{"work_mem", "16MB"},
	{"temp_file_limit", "64MB"},
	{"lock_timeout", "2s"},
	// A leader plus one worker per query, the process count the cluster's
	// memory is sized for (config.Runner's MaxParallelWorkers). A real bound:
	// the SQL validator does not admit SET.
	{"max_parallel_workers_per_gather", "1"},
	// Stops a backend whose client vanished without a cancel (runner crash,
	// dropped network) within this interval instead of at statement_timeout,
	// so it does not hold a memory cap's worth of the cluster.
	{"client_connection_check_interval", "250ms"},
}

// authorDefaults are the game-script role's. A build can take longer than a
// participant's five seconds; its bound is the caller's context deadline
// (provisioning.Games.Build), which the script cannot `SET` away. The explicit
// 0 resets any value set by hand on the next deploy.
var authorDefaults = [][2]string{
	{"statement_timeout", "0"},
	{"idle_in_transaction_session_timeout", "30s"},
}

// PrepareCluster makes the cluster ready to host participant databases. It runs
// on every deploy, so it is idempotent, and it is the only place the roles are
// defined: rerunning it is how a new restriction reaches an existing cluster.
func PrepareCluster(ctx context.Context, cluster Cluster, roles Roles) error {
	// One transaction, so a failed run leaves the cluster as it was. The lock
	// is transaction-scoped because a session lock over a pool could be
	// released on another connection, or never.
	tx, err := cluster.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin preparing the cluster: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(prepareLock)); err != nil {
		return fmt.Errorf("wait for another run to finish: %w", err)
	}

	if err := prepare(ctx, tx, roles); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish preparing the cluster: %w", err)
	}
	return nil
}

// roleSpec is one role as the deploy states it.
type roleSpec struct {
	name            string
	password        string
	connectionLimit int
	defaults        [][2]string
}

// prepare does the work, inside the caller's transaction.
func prepare(ctx context.Context, conn Conn, roles Roles) error {
	// Read-only is a default for the reader, not a boundary.
	readerDefaults := append(append([][2]string{}, sessionDefaults...),
		[2]string{"default_transaction_read_only", "on"})

	for _, role := range []roleSpec{
		{RoleReader, roles.ReaderPassword, connectionLimit, readerDefaults},
		{RoleWriter, roles.WriterPassword, connectionLimit, sessionDefaults},
		{RoleAuthor, roles.AuthorPassword, authorConnectionLimit, authorDefaults},
	} {
		if err := prepareRole(ctx, conn, role); err != nil {
			return fmt.Errorf("preparing %s: %w", role.name, err)
		}
	}
	return keepRolesOutOfMaintenanceDatabases(ctx, conn)
}

// prepareRole creates the role if missing and then states every attribute,
// including the negative ones, so a hand-edited role (say, granted CREATEDB
// during a contest) is put back by the next deploy.
func prepareRole(ctx context.Context, conn Conn, role roleSpec) error {
	name := role.name

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
		name, role.connectionLimit, role.password)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, alter); err != nil {
		return fmt.Errorf("setting the role's attributes: %w", err)
	}
	if err := stripMemberships(ctx, conn, name); err != nil {
		return err
	}

	for _, setting := range role.defaults {
		// The setting's name is a package constant, never input.
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

// stripMemberships revokes every role this one is a member of. Negative
// attributes do not cover memberships: a NOSUPERUSER member of
// pg_execute_server_program can still run programs on the server. None of
// these roles should be a member of anything, so all memberships go, including
// any granted by an older game script that ran as superuser.
func stripMemberships(ctx context.Context, conn Conn, name string) error {
	// An empty answer means no memberships and no second round trip.
	revokes, err := formatted(ctx, conn,
		`SELECT coalesce(
			string_agg(format('REVOKE %I FROM %I', granted.rolname, $1::text), '; '), '')
		 FROM pg_auth_members m
		 JOIN pg_roles granted ON granted.oid = m.roleid
		 WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = $1::text)`, name)
	if err != nil {
		return err
	}
	if revokes == "" {
		return nil
	}
	if _, err := conn.Exec(ctx, revokes); err != nil {
		return fmt.Errorf("taking back the role's memberships (%s): %w", revokes, err)
	}
	return nil
}

// keepRolesOutOfMaintenanceDatabases revokes CONNECT from PUBLIC on the
// cluster's own databases and the provisioner's, which covers every role,
// including one added later. It does not separate participants from each
// other: they share a role, and the Query Runner takes the database name from
// game_instances, never from the client.
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

// formatted asks the server to build a statement with format's %I and %L. DDL
// takes no bound parameters, and hand-rolled quoting could let a password with
// a quote end the literal early.
func formatted(ctx context.Context, conn Conn, query string, args ...any) (string, error) {
	var statement string
	if err := conn.QueryRow(ctx, query, args...).Scan(&statement); err != nil {
		return "", fmt.Errorf("building the statement: %w", err)
	}
	return statement, nil
}
