package gamedb

import (
	"context"
	"fmt"

	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
)

// HardenDatabase applies the per-database half of the boundary. It must run
// connected to the database being hardened. Every change is a privilege, not a
// setting, so it holds against SQL the validator never saw. CREATE DATABASE …
// TEMPLATE copies the ACLs to every instance; a database built before a
// restriction was added receives it from Provisioner.Harden.
func HardenDatabase(ctx context.Context, conn Conn) error {
	if err := hideSensitiveCatalogs(ctx, conn); err != nil {
		return err
	}
	if err := revokeSensitiveFunctions(ctx, conn); err != nil {
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
// rather than the game, using the same list the sqlpolicy validator refuses.
// It asks about any column because PostgreSQL grants pg_subscription to PUBLIC
// column by column, which a table-level check does not see.
func hideSensitiveCatalogs(ctx context.Context, conn Conn) error {
	return revokeFromPublic(ctx, conn,
		`SELECT coalesce(
			string_agg(format('REVOKE SELECT ON %s.%I FROM PUBLIC', c.relnamespace::regnamespace, c.relname), '; '), '')
		 FROM pg_class c
		 WHERE c.relnamespace IN ('pg_catalog'::regnamespace, 'information_schema'::regnamespace)
		   AND c.relname = ANY($1)
		   AND has_any_column_privilege('public', c.oid, 'SELECT')`,
		sqlpolicy.SensitiveCatalogs())
}

// revokeSensitiveFunctions takes EXECUTE on every overload of the functions
// sqlpolicy lists away from PUBLIC, which is where the participant roles get
// it.
func revokeSensitiveFunctions(ctx context.Context, conn Conn) error {
	return revokeFromPublic(ctx, conn,
		`SELECT coalesce(
			string_agg(format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', p.oid::regprocedure), '; '), '')
		 FROM pg_proc p
		 WHERE p.pronamespace = 'pg_catalog'::regnamespace
		   AND p.proname = ANY($1)
		   AND has_function_privilege('public', p.oid, 'EXECUTE')`,
		sqlpolicy.SensitiveFunctions())
}

// revokeFromPublic runs the REVOKEs the query writes from this server's
// catalog for the names given. A name the server lacks (another PostgreSQL
// version, no pg_stat_statements) therefore yields none, and neither does one
// PUBLIC no longer holds: the deploy visits every database each time, and a
// hardened one must cost a read, not a catalog write per name.
func revokeFromPublic(ctx context.Context, conn Conn, query string, names []string) error {
	revokes, err := formatted(ctx, conn, query, names)
	if err != nil {
		return err
	}
	if revokes == "" {
		return nil
	}
	if _, err := conn.Exec(ctx, revokes); err != nil {
		return fmt.Errorf("revoking from PUBLIC: %w", err)
	}
	return nil
}
