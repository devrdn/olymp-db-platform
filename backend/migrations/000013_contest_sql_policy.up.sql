-- The SQL access policy is a setting of the olympiad (section 4.1), and until
-- now it existed only as a Go type. It has to be stored, because two layers
-- are built from it — the validator that refuses statements and the GRANTs a
-- template is built with — and neither can read a value nobody wrote down.

ALTER TABLE contests
    ADD COLUMN sql_mode text NOT NULL DEFAULT 'read_only'
        CHECK (sql_mode IN ('read_only', 'read_write')),
    ADD COLUMN writable_tables text[] NOT NULL DEFAULT '{}',
    ADD COLUMN allow_create_view boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_own_tables  boolean NOT NULL DEFAULT false,
    ADD COLUMN allow_temp_tables boolean NOT NULL DEFAULT false,
    -- Structural catalogues are readable by default: looking at the shape of a
    -- table is part of the exercise. The flag exists to turn that off.
    ADD COLUMN allow_catalog     boolean NOT NULL DEFAULT true;

-- The same coherence the Go type enforces, stated where the data lives. Every
-- one of these needs the writer role and a GRANT, so granting one under
-- read_only would produce a policy that means one thing to the validator and
-- another to the template builder — the drift the whole arrangement exists to
-- prevent.
ALTER TABLE contests ADD CONSTRAINT contests_policy_coherent CHECK (
    sql_mode = 'read_write' OR (
        cardinality(writable_tables) = 0
        AND NOT allow_create_view
        AND NOT allow_own_tables
        AND NOT allow_temp_tables
    )
);

-- A table name is interpolated into GRANT statements when a template is built,
-- because SQL has no way to bind an identifier. The application refuses
-- anything that is not a plain name; so does the column, because the
-- application is one release away from forgetting.
--
-- A domain rather than a CHECK on the column: PostgreSQL does not allow a
-- subquery in a CHECK, and without one there is no way to reach inside an
-- array — while a domain constraint is applied to every element on the way in,
-- which is exactly the rule being stated.
CREATE DOMAIN plain_identifier AS text
    CHECK (VALUE ~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$');

ALTER TABLE contests
    ALTER COLUMN writable_tables TYPE plain_identifier[] USING writable_tables::plain_identifier[];
