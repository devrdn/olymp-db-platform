package gamedb

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// HardenDatabase applies the per-database half of the boundary. It must run
// connected to the database being hardened. Both changes are privileges, not
// settings, so they hold against SQL the validator never saw. It runs once on a
// template; CREATE DATABASE … TEMPLATE copies the ACLs to every instance.
func HardenDatabase(ctx context.Context, conn Conn) error {
	if err := hideSensitiveCatalogs(ctx, conn); err != nil {
		return err
	}
	// Default since PostgreSQL 15, but stated for older clusters and for a
	// template author who granted it back.
	if _, err := conn.Exec(ctx, `REVOKE CREATE ON SCHEMA public FROM PUBLIC`); err != nil {
		return fmt.Errorf("revoking create on the public schema: %w", err)
	}
	return nil
}

// hideSensitiveCatalogs revokes the catalogs that describe the installation
// rather than the game, using the same list the sqlpolicy validator refuses. A
// relation missing on this server (another PostgreSQL version, no
// pg_stat_statements) is skipped.
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
