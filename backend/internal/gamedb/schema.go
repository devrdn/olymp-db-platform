package gamedb

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// schemaQuery reads one database's shape: its relations, their columns in
// declaration order, each column's type and nullability, and the table a
// foreign key points at.
//
// pg_catalog rather than information_schema. The views in information_schema
// are defined over the same tables with several joins and a privilege filter
// each, and are markedly slower for exactly this shape of question; more to
// the point, `attnum` is what gives declaration order, and information_schema
// exposes it as `ordinal_position` only after doing the work to compute it.
//
// The lateral join takes the *first* foreign key a column participates in.
// A column in two foreign keys is legal and vanishingly rare in a game, and
// the panel has one line per column to say it in — `ORDER BY con.oid` at
// least makes which one it is deterministic rather than whatever the planner
// happened to produce.
const schemaQuery = `
SELECT c.relname,
       a.attname,
       format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       COALESCE(ft.relname, '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
LEFT JOIN LATERAL (
    SELECT fc.relname
    FROM pg_constraint con
    JOIN pg_class fc ON fc.oid = con.confrelid
    WHERE con.conrelid = c.oid AND con.contype = 'f' AND a.attnum = ANY (con.conkey)
    ORDER BY con.oid
    LIMIT 1
) ft ON true
WHERE n.nspname = 'public' AND c.relkind = ANY ($1)
ORDER BY c.relname, a.attnum
LIMIT $2`

// gameRelkinds are the relation kinds a participant can write a SELECT
// against: ordinary and partitioned tables, views and materialised views.
// Indexes, sequences, composite types and TOAST tables are how the game is
// stored rather than what it is about.
var gameRelkinds = []byte{'r', 'p', 'v', 'm'}

// maxSchemaRows bounds the catalogue read where the rows arrive, rather than
// after they are all in memory (CLAUDE.md rule 12). The init script is an
// organiser's own SQL, so the number of columns in a game is not this
// platform's to assume; the shape-level bounds live in provisioning, and this
// is the memory guard underneath them.
const maxSchemaRows = provisioning.MaxSchemaTables * provisioning.MaxSchemaColumns

// ReadSchema describes one game database, for the console's schema panel.
//
// Read from an instance and never from a template — connecting to a template
// is what makes `CREATE DATABASE ... TEMPLATE` fail for everybody else
// (SQLSTATE 55006). provisioning.SchemaReader is the caller that guarantees
// that, and the reason the answer is cached: an instance database carries
// `CONNECTION LIMIT 2` (see grants.go), which this borrows one of for the
// length of one catalogue read.
func (p *Provisioner) ReadSchema(ctx context.Context, database string) (provisioning.Schema, error) {
	if !sqlpolicy.PlainIdentifier(database) {
		return provisioning.Schema{}, fmt.Errorf("%w: %q", ErrBadName, database)
	}

	// As the provisioning role: this reads an instance's catalogue, which is
	// nobody's uploaded SQL and needs no containment.
	conn, err := p.connect(ctx, p.base.User, database)
	if err != nil {
		return provisioning.Schema{}, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	// This connection is opened for one statement and closed, so a session
	// timeout is the whole of its life. Ten seconds is the core API's own
	// figure and generous for a catalogue read; what it rules out is this
	// holding one of an instance's two connections open indefinitely because
	// the cluster is wedged (CLAUDE.md rule 15).
	if _, err := conn.Exec(ctx, `SET statement_timeout = '10s'`); err != nil {
		return provisioning.Schema{}, fmt.Errorf("bound the schema read of %s: %w", database, err)
	}

	rows, err := conn.Query(ctx, schemaQuery, gameRelkinds, maxSchemaRows+1)
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("read the schema of %s: %w", database, err)
	}
	defer rows.Close()

	var (
		schema provisioning.Schema
		seen   int
	)
	for rows.Next() {
		var table, column, columnType, references string
		var nullable bool
		if err := rows.Scan(&table, &column, &columnType, &nullable, &references); err != nil {
			return provisioning.Schema{}, fmt.Errorf("read the schema of %s: %w", database, err)
		}

		seen++
		if seen > maxSchemaRows {
			// One row past the bound is how the query says there were more.
			// The rest are not read.
			schema.Truncated = true
			break
		}

		// Rows arrive grouped by relation (ORDER BY c.relname), so the table
		// being filled is always the last one appended.
		if len(schema.Tables) == 0 || schema.Tables[len(schema.Tables)-1].Name != table {
			schema.Tables = append(schema.Tables, provisioning.Table{Name: table})
		}
		last := &schema.Tables[len(schema.Tables)-1]
		last.Columns = append(last.Columns, provisioning.Column{
			Name: column, Type: columnType, Nullable: nullable, References: references,
		})
	}
	if err := rows.Err(); err != nil && !schema.Truncated {
		return provisioning.Schema{}, fmt.Errorf("read the schema of %s: %w", database, err)
	}
	return schema, nil
}
