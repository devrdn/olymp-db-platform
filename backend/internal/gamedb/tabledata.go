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

// LoadTableData copies CSV rows into one table of an already-built template
// with COPY ... FROM STDIN. It runs once per table after BuildTemplate
// succeeds.
//
// It connects as the provisioning role, because by now the template has been
// taken back from game_author, CONNECT included.
//
// data must be exactly what CSV COPY expects: no header line, any filtering
// already done by the caller.
func (p *Provisioner) LoadTableData(ctx context.Context, database, table string, columns []string, data io.Reader) error {
	if !sqlpolicy.PlainIdentifier(database) || !sqlpolicy.PlainIdentifier(table) {
		return fmt.Errorf("%w: %q or %q", ErrBadName, database, table)
	}
	if p.authorPassword == "" {
		// Not used to authenticate here, but its absence marks a
		// maintenance-only Provisioner, which must not build anything.
		return ErrNoAuthorCredential
	}

	// One table can take most of a build, so it gets the build's own
	// deadline (CLAUDE.md rule 15).
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

// TableDataError is PostgreSQL's verdict on one table's data, such as a
// violated constraint. Unlike ScriptError it names a table, not a script line.
type TableDataError struct {
	Table   string
	Message string
	Detail  string
	Hint    string
	// Where is PostgreSQL's CONTEXT, e.g. "COPY <table>, line N, column
	// <name>: ...", the source of the row number shown to the organiser.
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

// ScriptRejection satisfies provisioning.ScriptFailure.
func (e *TableDataError) ScriptRejection() string { return e.Error() }

// tableDataFailure turns PostgreSQL's verdict on the data into a
// TableDataError the organiser may read. Anything else (a failed connection, a
// driver error) is wrapped, so it is reported as an internal failure.
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
