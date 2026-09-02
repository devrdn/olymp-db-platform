ALTER TABLE contest_sql_policies DROP CONSTRAINT IF EXISTS contest_sql_policies_coherent;

ALTER TABLE contest_sql_policies
    ALTER COLUMN writable_tables TYPE text[] USING writable_tables::text[];

DROP DOMAIN IF EXISTS plain_table_name;

CREATE DOMAIN plain_identifier AS text
    CHECK (VALUE ~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$');

ALTER TABLE contest_sql_policies
    ADD CONSTRAINT contest_sql_policies_writes_need_write_mode
        CHECK (mode = 'read_write' OR cardinality(writable_tables) = 0);

ALTER TABLE contests
    ADD COLUMN sql_mode text NOT NULL DEFAULT 'read_only'
        CHECK (sql_mode IN ('read_only', 'read_write')),
    ADD COLUMN writable_tables plain_identifier[] NOT NULL DEFAULT '{}',
    ADD COLUMN allow_create_view boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_own_tables  boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_temp_tables boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_catalog     boolean NOT NULL DEFAULT true;

ALTER TABLE contests ADD CONSTRAINT contests_policy_coherent CHECK (
    sql_mode = 'read_write' OR (
        cardinality(writable_tables) = 0
        AND NOT allow_create_view
        AND NOT allow_own_tables
        AND NOT allow_temp_tables
    )
);
