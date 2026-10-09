package gamedb

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// schemaQuery reads one database's relations, their columns in declaration
// order, each column's type and nullability, and the table a foreign key
// points at. pg_catalog is used because information_schema is markedly slower
// here. A column in two foreign keys shows the first by oid, so the answer is
// deterministic.
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

// gameRelkinds are the relation kinds a participant can SELECT from: tables,
// partitioned tables, views and materialised views.
var gameRelkinds = []byte{'r', 'p', 'v', 'm'}

// maxSchemaRows bounds the catalogue read as rows arrive (CLAUDE.md rule 12);
// the init script is an organiser's SQL, so the column count is unbounded.
const maxSchemaRows = provisioning.MaxSchemaTables * provisioning.MaxSchemaColumns

// ReadSchema describes one game database, for the console's schema panel.
//
// It must read an instance, never a template: a connection to a template makes
// `CREATE DATABASE ... TEMPLATE` fail (55006). provisioning.SchemaReader
// guarantees that and caches the answer. It connects as the superuser
// provisioning role, so it never takes a participant's connection slot.
func (p *Provisioner) ReadSchema(ctx context.Context, database string) (provisioning.Schema, error) {
	if !sqlpolicy.PlainIdentifier(database) {
		return provisioning.Schema{}, fmt.Errorf("%w: %q", ErrBadName, database)
	}

	conn, err := p.connect(ctx, p.base.User, database)
	if err != nil {
		return provisioning.Schema{}, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	// Keeps a wedged cluster from holding this backend open (CLAUDE.md rule 15).
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
			// The query asks for one row past the bound to detect more.
			schema.Truncated = true
			break
		}

		// Rows arrive grouped by relation.
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
