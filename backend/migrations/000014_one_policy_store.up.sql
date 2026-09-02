-- Undo a duplicate. Migration 13 put the SQL access policy on `contests`,
-- unaware that `contest_sql_policies` had held it since migration 2 — a
-- second home for the value two layers are built from, which is the drift the
-- whole arrangement exists to prevent. The older table wins: it is the one
-- with a repository, a disk quota and callers.

ALTER TABLE contests DROP CONSTRAINT IF EXISTS contests_policy_coherent;

ALTER TABLE contests
    DROP COLUMN IF EXISTS allow_catalog,
    DROP COLUMN IF EXISTS allow_temp_tables,
    DROP COLUMN IF EXISTS allow_own_tables,
    DROP COLUMN IF EXISTS allow_create_view,
    DROP COLUMN IF EXISTS writable_tables,
    DROP COLUMN IF EXISTS sql_mode;

-- What migration 13 got right moves to where the policy actually lives.
--
-- A writable table name is interpolated into GRANT statements when a template
-- is built, because SQL has no way to bind an identifier. The application
-- refuses anything that is not a plain name; the column should too, and a
-- domain is how — PostgreSQL allows no subquery in a CHECK, so there is no
-- other way to reach inside an array, while a domain constraint is applied to
-- every element on the way in.
-- Widened from migration 13's version, which refused a qualified name and so
-- refused `public.evidence` — a spelling the platform already stores, because
-- a contest's game schema need not be `public`. Two dots stay refused: that
-- would be a database reference, and this cluster has no business with those.
DROP DOMAIN IF EXISTS plain_identifier;

CREATE DOMAIN plain_table_name AS text CHECK (
    VALUE ~ '^[A-Za-z_][A-Za-z0-9_]{0,62}(\.[A-Za-z_][A-Za-z0-9_]{0,62})?$'
);

ALTER TABLE contest_sql_policies
    ALTER COLUMN writable_tables TYPE plain_table_name[]
        USING writable_tables::plain_table_name[];

-- The existing check covered only the writable tables, so `read_only` with
-- `allow_own_tables` was accepted by the database and refused by
-- Policy.Validate — the two disagreeing about the same value. Every option
-- below needs the writer role and a GRANT, so none of them means anything
-- under read_only.
ALTER TABLE contest_sql_policies
    DROP CONSTRAINT IF EXISTS contest_sql_policies_writes_need_write_mode;

ALTER TABLE contest_sql_policies ADD CONSTRAINT contest_sql_policies_coherent CHECK (
    mode = 'read_write' OR (
        cardinality(writable_tables) = 0
        AND NOT allow_create_view
        AND NOT allow_own_tables
        AND NOT allow_temp_tables
    )
);
