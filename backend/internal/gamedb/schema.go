package gamedb

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
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

// keysQuery reads the primary key and the foreign keys of the relations
// readColumns kept ($3), each key's columns in the constraint's order rather
// than the table's. A foreign key is listed only when it points into the
// public schema, where the diagram has a box to draw it to, and only on the
// table that declared it: the clones PostgreSQL keeps for partitions
// (conparentid) would draw one arrow many times. Every primary key sorts
// before every foreign key, so the row bound cuts only foreign keys.
const keysQuery = `
SELECT c.relname,
       con.contype = 'p',
       con.conname,
       ARRAY(SELECT a.attname::text
             FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, position)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
             ORDER BY k.position),
       COALESCE(fc.relname, ''),
       ARRAY(SELECT a.attname::text
             FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, position)
             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
             ORDER BY k.position)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_class fc ON fc.oid = con.confrelid
LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
WHERE n.nspname = 'public' AND c.relkind = ANY ($1) AND c.relname = ANY ($3)
  AND (con.contype = 'p'
       OR (con.contype = 'f' AND fn.nspname = 'public' AND con.conparentid = 0))
ORDER BY con.contype = 'p' DESC, c.relname, con.conname
LIMIT $2`

// gameRelkinds are the relation kinds a participant can SELECT from: tables,
// partitioned tables, views and materialised views.
var gameRelkinds = []byte{'r', 'p', 'v', 'm'}

// maxSchemaRows and maxKeyRows bound the catalogue reads as rows arrive
// (CLAUDE.md rule 12); the init script is an organiser's SQL, so the column
// and constraint counts are unbounded.
const (
	maxSchemaRows = provisioning.MaxSchemaTables * provisioning.MaxSchemaColumns
	maxKeyRows    = provisioning.MaxSchemaTables + provisioning.MaxDocumentForeignKeys
)

// ReadSchema describes one game database, for the console's schema panel and
// diagram.
//
// database is an instance, or a template only from provisioning.Games.describe,
// whose comment says when reading a template is safe. It connects as the superuser provisioning role, so it never
// takes a participant's connection slot.
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

	// One snapshot for both reads, so no key names a column the first missed.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("read the schema of %s: %w", database, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	schema, err := readColumns(ctx, tx)
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("read the schema of %s: %w", database, err)
	}
	if err := readKeys(ctx, tx, &schema); err != nil {
		return provisioning.Schema{}, fmt.Errorf("read the keys of %s: %w", database, err)
	}
	return schema, nil
}

func readColumns(ctx context.Context, conn pgx.Tx) (provisioning.Schema, error) {
	rows, err := conn.Query(ctx, schemaQuery, gameRelkinds, maxSchemaRows+1)
	if err != nil {
		return provisioning.Schema{}, err
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
			return provisioning.Schema{}, err
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
		return provisioning.Schema{}, err
	}
	return schema, nil
}

// readKeys attaches each relation's keys to the tables readColumns kept, and
// stops at MaxDocumentForeignKeys foreign keys.
func readKeys(ctx context.Context, conn pgx.Tx, schema *provisioning.Schema) error {
	byName := make(map[string]int, len(schema.Tables))
	names := make([]string, len(schema.Tables))
	for i, table := range schema.Tables {
		byName[table.Name] = i
		names[i] = table.Name
	}

	rows, err := conn.Query(ctx, keysQuery, gameRelkinds, maxKeyRows+1, names)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen, foreign, stopped := 0, 0, false
	for rows.Next() {
		var (
			table, name, referenced string
			primary                 bool
			columns, referencedCols []string
		)
		if err := rows.Scan(&table, &primary, &name, &columns, &referenced, &referencedCols); err != nil {
			return err
		}

		seen++
		if seen > maxKeyRows {
			stopped = true
			break
		}

		i, kept := byName[table]
		if !kept {
			continue
		}
		if primary {
			schema.Tables[i].PrimaryKey = columns
			continue
		}
		foreign++
		if foreign > provisioning.MaxDocumentForeignKeys {
			stopped = true
			break
		}
		schema.Tables[i].ForeignKeys = append(schema.Tables[i].ForeignKeys, provisioning.ForeignKey{
			Name: name, Columns: columns, ReferencedTable: referenced, ReferencedColumns: referencedCols,
		})
	}
	if stopped {
		schema.Truncated = true
		return nil
	}
	return rows.Err()
}
