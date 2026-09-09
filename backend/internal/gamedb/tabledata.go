package gamedb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// LoadTableData copies data into one table of an already-built template,
// through COPY ... FROM STDIN WITH (FORMAT csv) — the same protocol path an
// uploaded dump's own rows go through (runScript's CopyFrom call), never a
// row-at-a-time INSERT. Called once per table with data, after BuildTemplate
// has returned with no error (provisioning.Games.loadTableData, at the seam
// finishDefinitionBuild's own doc names).
//
// It connects as the provisioning role itself (p.base.User), not as
// RoleAuthor: by the time this runs, BuildTemplate's own fill has already
// withdrawn every privilege game_author was lent to build the schema
// (takeTheTemplateBackFromTheAuthor), including CONNECT — a second
// connection authenticated as game_author would simply be refused a login.
// The provisioning role is the one this package already trusts with
// database-wide DDL (CREATE DATABASE, the grants in grants.go, hardening in
// database.go), so it is also the one still able to reach a template between
// the schema build finishing and the game being marked ready.
//
// data is expected to be exactly the rows PostgreSQL's own CSV COPY format
// wants — no header line, and whatever filtering the caller's own storage
// needed (a header row skipped, a deleted row's line left out) already done
// before this is called; this package knows nothing about either.
func (p *Provisioner) LoadTableData(ctx context.Context, database, table string, columns []string, data io.Reader) error {
	if !sqlpolicy.PlainIdentifier(database) || !sqlpolicy.PlainIdentifier(table) {
		return fmt.Errorf("%w: %q or %q", ErrBadName, database, table)
	}
	if p.authorPassword == "" {
		// Not actually used to authenticate this call (see the doc above),
		// but its absence means this Provisioner was built for maintenance
		// only (NewProvisioner's own doc: DropIdle-style commands pass "").
		// Refusing here rather than reaching PostgreSQL with a role that was
		// never meant to build anything keeps the same guarantee
		// BuildTemplate's own check gives.
		return ErrNoAuthorCredential
	}

	// The same statement_timeout discipline runScript applies to the schema
	// build (CLAUDE.md rule 15): a whole table's worth of rows can plausibly
	// be most of a build's own time, so the bound has to be at least as
	// generous as the build's own deadline, and there is no smaller
	// principled number to give it instead.
	ctx, cancel := context.WithTimeout(ctx, p.buildTimeout)
	defer cancel()

	conn, err := p.connect(ctx, p.base.User, database)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	timeoutMS := p.buildTimeout.Milliseconds()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET statement_timeout = %d`, timeoutMS)); err != nil {
		return tableDataFailure(table, err)
	}

	quotedColumns := make([]string, len(columns))
	for i, c := range columns {
		quotedColumns[i] = sqlpolicy.QuoteIdentifier(c)
	}
	copySQL := `COPY public.` + sqlpolicy.QuoteIdentifier(table) +
		` (` + strings.Join(quotedColumns, ", ") + `) FROM STDIN WITH (FORMAT csv)`

	if _, err := conn.PgConn().CopyFrom(ctx, data, copySQL); err != nil {
		return tableDataFailure(table, err)
	}
	return nil
}

// TableDataError is PostgreSQL's own verdict on one table's data — a
// constraint COPY found violated, a value that did not fit its column after
// all. Its own type beside ScriptError rather than reused, because it names
// a table rather than a line of a script: the two failures are located
// differently, and provisioning.Games.loadTableData's own caller
// (finishDefinitionBuild) has no script line to report here at all.
type TableDataError struct {
	Table   string
	Message string
	Detail  string
	Hint    string
	// Where is PostgreSQL's own CONTEXT for a COPY failure — typically
	// "COPY <table>, line N, column <name>: ..." — which is where the row
	// number organiser-facing text comes from; not reconstructed by this
	// package, because PostgreSQL already knows exactly which of the rows it
	// was reading when it refused one.
	Where string
}

func (e *TableDataError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "table %q: the data was refused: %s", e.Table, e.Message)
	if e.Detail != "" {
		b.WriteString("\nDETAIL: " + e.Detail)
	}
	if e.Hint != "" {
		b.WriteString("\nHINT: " + e.Hint)
	}
	if e.Where != "" {
		b.WriteString("\nCONTEXT: " + e.Where)
	}
	return b.String()
}

// ScriptRejection satisfies provisioning.ScriptFailure — see that
// interface's own doc for why this is opted into rather than discovered by a
// type switch.
func (e *TableDataError) ScriptRejection() string { return e.Error() }

// tableDataFailure classifies a COPY failure the same way scriptFailure
// classifies a script's own: PostgreSQL's verdict on the data becomes a
// TableDataError the organiser may read, and everything else — a connection
// that never opened, a driver failure of ours — is wrapped instead, so
// finishDefinitionBuild reports it as BuildFailedInternally rather than
// putting our own infrastructure in front of the person who uploaded a CSV.
func tableDataFailure(table string, err error) error {
	var connect *pgconn.ConnectError
	if errors.As(err, &connect) {
		return fmt.Errorf("load %s's own data: %w", table, err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("load %s's own data: %w", table, err)
	}
	return &TableDataError{Table: table, Message: pgErr.Message, Detail: pgErr.Detail, Hint: pgErr.Hint, Where: pgErr.Where}
}
