package gamedb

import (
	"context"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// HardenDatabase applies the per-database half of the boundary. It must run
// connected to the database being hardened.
//
// Two things happen here, and both are privileges rather than settings, which
// is what makes them hold against SQL the validator never saw.
//
// It is applied to a template, once, and reaches participants by being
// inherited: CREATE DATABASE … TEMPLATE copies the catalog, ACLs included. That
// inheritance is the whole mechanism — without it every instance would have to
// be hardened separately, and the one that was missed would look exactly like
// the others.
func HardenDatabase(ctx context.Context, conn Conn) error {
	if err := hideSensitiveCatalogs(ctx, conn); err != nil {
		return err
	}
	// PostgreSQL 15 stopped granting CREATE on the public schema to PUBLIC, so
	// this is usually already true. Stated anyway: it costs one statement, and
	// it survives both an older cluster and a template whose author granted it
	// back while getting something to work.
	if _, err := conn.Exec(ctx, `REVOKE CREATE ON SCHEMA public FROM PUBLIC`); err != nil {
		return fmt.Errorf("revoking create on the public schema: %w", err)
	}
	return nil
}

// hideSensitiveCatalogs revokes the catalogs that describe the installation
// rather than the game.
//
// The list comes from internal/sqlpolicy, which is the same list its validator
// refuses — one description, two layers, exactly as section 4.1 requires. A
// relation that does not exist on this server is skipped rather than fatal:
// the list spans PostgreSQL versions and one extension's view, and a cluster
// without pg_stat_statements installed is not a misconfigured cluster.
func hideSensitiveCatalogs(ctx context.Context, conn Conn) error {
	for _, name := range sqlpolicy.SensitiveCatalogs() {
		var present bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'pg_catalog' AND c.relname = $1)`, name).Scan(&present); err != nil {
			return fmt.Errorf("looking for pg_catalog.%s: %w", name, err)
		}
		if !present {
			continue
		}

		statement, err := formatted(ctx, conn,
			`SELECT format('REVOKE SELECT ON pg_catalog.%I FROM PUBLIC', $1::text)`, name)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("revoking select on pg_catalog.%s: %w", name, err)
		}
	}
	return nil
}

// quoteIdentifier spells a name the way PostgreSQL does in its own dumps.
//
// Needed where a name cannot be a bound parameter and cannot be built by the
// server either — CREATE DATABASE and DROP DATABASE run outside a transaction
// and before there is a connection to the database in question. Every name
// this is given is one the platform generated, never a participant's, but it
// is quoted regardless: the day that stops being true, this is what decides
// whether it matters.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
