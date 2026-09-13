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

// RoleAuthor is who an organiser's game script runs as.
//
// A third role, and the only one the Core API ever authenticates as. It exists
// because the route that accepts a script is gated by a contest-scoped
// permission — any manager of any single contest — while the cluster it runs
// on holds every other contest's template and every participant's database.
// Run with the provisioning role's own privileges, a game script was therefore
// arbitrary SQL as a superuser, bought with a permission on one draft contest.
//
// Changing hats is not enough: `SET ROLE` is undone by a `RESET ROLE` inside
// the script itself, which is why this is a role with a password of its own
// and a separate connection, and not a setting on the provisioner's.
const RoleAuthor = "game_author"

// connectionLimit is the last line under the Query Runner's semaphore.
//
// Reaching it means admission control has already failed, so it is set high
// enough never to be the thing that shapes normal load and low enough that a
// broken runner cannot exhaust the cluster's connections.
const connectionLimit = 60

// authorConnectionLimit is how many game scripts may be running at once.
//
// Much smaller than the participants', because the population is different:
// only the Core API authenticates as this role, and only while building a
// template. A handful is room for every provisioning worker and a retry, and
// nothing beyond that is anything but a leak.
const authorConnectionLimit = 8

// Roles carries the credentials the three non-provisioning roles are given.
type Roles struct {
	ReaderPassword string
	WriterPassword string
	// AuthorPassword is the Core API's own credential on this cluster: the
	// role an organiser's game script runs as. See RoleAuthor.
	AuthorPassword string
}

// Conn is the part of a connection this package uses.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Cluster is a connection that can also open a transaction and read many
// rows — what preparing the cluster and reporting on it need, and what
// hardening one database does not.
type Cluster interface {
	Conn
	Begin(ctx context.Context) (pgx.Tx, error)
	// Query is used only to read the cluster's own catalogue for a whole list
	// at once (Provisioner.DatabaseSizes). Nothing that touches a
	// participant's data goes through here — that is the Query Runner's.
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// prepareLock is the advisory lock every run of PrepareCluster takes.
//
// An arbitrary but fixed number, and the only thing that matters about it is
// that no other advisory lock in this system uses it. Role DDL in PostgreSQL
// updates a shared catalogue row, and two sessions altering the same role at
// once get `tuple concurrently updated` — SQLSTATE XX000, an internal error
// that reads like a real fault. Two replicas of the job, a retry after a
// timeout, or a deploy racing somebody's manual run are all ordinary; they
// should queue, not collide.
const prepareLock = 8_531_204_477_119_003_1

// sessionDefaults are applied to both participant roles with ALTER ROLE … SET.
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
	// One parallel worker at most, so a participant query occupies a leader
	// plus one worker process rather than the server default of two — the
	// process count the game cluster's memory is sized for (config.Runner's
	// MaxParallelWorkersPerQuery). Parallel query stays on, which the steady
	// state wants; the cap only bounds how many processes one query spreads
	// across. Unlike statement_timeout above this is a real bound for a
	// participant, not only a default: SET is not a statement the SQL
	// validator admits, so a participant cannot raise it.
	{"max_parallel_workers_per_gather", "1"},
}

// authorDefaults are the game-script role's, and deliberately not the
// participants'.
//
// A build is one long run of DDL and INSERTs against a database nobody else is
// using; a participant's query is a stranger's SELECT against a database
// somebody is sitting in front of. The five-second statement_timeout that is
// right for the second would fail every olympiad whose data takes longer than
// that to load. What bounds a build instead is the deadline its caller puts on
// the context (provisioning.Games.Build), which is on the connection and so is
// not something the script can `SET` away.
//
// Stated as an explicit 0 rather than left out, for the same reason
// prepareRole's ALTER names the attributes a role must *not* have: a value
// somebody set by hand during a contest is put back by the next deploy.
var authorDefaults = [][2]string{
	{"statement_timeout", "0"},
	{"idle_in_transaction_session_timeout", "30s"},
}

// PrepareCluster makes the cluster ready to host participant databases.
//
// Idempotent, because it runs on every deploy and an already-prepared cluster
// is the normal case. It is also the only place the roles are defined, so
// running it after an upgrade is how a new restriction reaches a cluster that
// already exists — which is why it is a program rather than an init script
// that the image runs once and silently skips ever after.
func PrepareCluster(ctx context.Context, cluster Cluster, roles Roles) error {
	// One transaction for the whole thing, holding an advisory lock: role DDL
	// is transactional in PostgreSQL, so a run that fails half-way leaves the
	// cluster as it was rather than half-declared. The lock is released by the
	// commit, which is why it is the transaction-scoped variety — a session
	// lock over a pool would be taken on one connection and released on
	// another, or not at all.
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

// roleSpec is one role as the deploy states it: what it authenticates with,
// how many connections it may hold at once, and what its sessions start with.
type roleSpec struct {
	name            string
	password        string
	connectionLimit int
	defaults        [][2]string
}

// prepare does the work, inside the caller's transaction.
func prepare(ctx context.Context, conn Conn, roles Roles) error {
	// The reader is the one role that starts read-only. It is a default and
	// not a boundary — see the package comment — but it is the correct default
	// for a contest that permits no writing at all.
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

// prepareRole creates the role if it is missing and then states, in full, what
// it may be.
//
// The ALTER is unconditional and lists every attribute, including the negative
// ones. A role that was hand-edited on a running cluster — granted CREATEDB to
// get something working during a contest — is put back by the next deploy,
// which is only true if the statement names what the role must *not* have as
// well as what it must.
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

// stripMemberships removes every role this one has been made a member of.
//
// The ALTER above states what a role must not *be*; this states what it must
// not *have*. They are different catalogues, and only the first is covered by
// naming the negative attributes: a role that reads as NOSUPERUSER
// NOCREATEDB NOCREATEROLE and is a member of pg_execute_server_program can
// still run a program on the server. PostgreSQL's five predefined roles hand
// out very nearly the list of things this package exists to refuse, so none of
// these three roles is ever meant to be a member of anything at all — which is
// what makes "revoke whatever is there" the right rule rather than a list to
// keep in step with PostgreSQL's.
//
// It matters most on upgrade. A cluster that ran an organiser's game script
// before the script had a role of its own ran it as a superuser, so a
// membership granted from inside one is a leftover the deploy has to take back
// — otherwise the new role is the old hole under a new name.
func stripMemberships(ctx context.Context, conn Conn, name string) error {
	// Built by the server, like every other statement here that carries an
	// identifier: the names come from the catalogue rather than from us, and
	// format's %I is the same quoting PostgreSQL uses in its own dumps. An
	// empty answer means the role is a member of nothing, which is the normal
	// case and costs no second round trip.
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

// keepRolesOutOfMaintenanceDatabases stops a participant's role — or the
// author's — connecting to anything that is not a game.
//
// It does not separate one participant from another — they share a role, and
// what keeps them apart is that the Query Runner takes the database name from
// game_instances and never from the client. What it does is keep the roles out
// of the cluster's own databases and out of the one the provisioner works in.
// Revoking from PUBLIC is what makes that cover every one of them, including a
// role added later.
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
