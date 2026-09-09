-- The table builder's own data (feat/game-table-builder, third task): a
-- table an organiser described structurally (migration 26) gets its rows
-- from a CSV file on the API host's volume, one file per table, through the
-- same internal/gamefile chunked-upload mechanics migration 24 gave a whole
-- SQL dump. This is the bookkeeping that lets that file survive the request
-- that started it, the same job game_uploads already does for a dump.
--
-- Deliberately its own table rather than a second row shape squeezed into
-- game_uploads: a dump upload replaces the *whole* game the moment it
-- completes (CompleteUpload bumps game_templates straight away), while a
-- table's CSV is just data for one table of an already-saved definition — it
-- never becomes the contest's game on its own, and completing one must not
-- touch game_templates at all. Folding the two into one table would mean
-- every read of game_uploads carrying a nullable table_name nobody but this
-- feature ever sets, and CompleteUpload's own upsertGame call would need a
-- branch for "actually, this one is not the game" — the two events belong to
-- two different tables for the same reason migration 26 gave definition_json
-- its own column instead of overloading init_script.
CREATE TABLE game_table_data (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id     uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    -- The table this file's rows belong to, exactly as the organiser spelled
    -- it in the definition (provisioning.Definition) — matched byte for byte
    -- against Definition.Tables[].Name at every call, never re-derived or
    -- folded here. Bounded the way PostgreSQL bounds an identifier itself
    -- (NAMEDATALEN - 1 = 63; see sqlpolicy.PlainIdentifier's own bound).
    table_name     text NOT NULL CHECK (length(table_name) BETWEEN 1 AND 63),
    declared_bytes bigint NOT NULL CHECK (declared_bytes > 0),
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    -- Filled in once CompleteTableUpload's own validation pass (header, every
    -- row's field count and column types) has run; null for the whole time a
    -- file is 'receiving', the same convention game_uploads.line_count keeps.
    line_count     bigint,
    -- Row numbers (1-based, header excluded, stable for the life of the
    -- file) an organiser deleted. A tombstone, not a rewrite of the file:
    -- provisioning.Games.DeleteTableRow's own doc explains why removing a
    -- row from a multi-gigabyte CSV must not mean rewriting it on every
    -- click. Bounded (CLAUDE.md rule 2) for the same reason declared_bytes
    -- is — this is a list reaching storage — at a size manual curation could
    -- never approach; provisioning.MaxTableDeletedRows carries the same
    -- number so the two cannot drift.
    deleted_rows   bigint[] NOT NULL DEFAULT '{}'
        CHECK (array_length(deleted_rows, 1) IS NULL OR array_length(deleted_rows, 1) <= 10000),
    status         text NOT NULL DEFAULT 'receiving'
        CHECK (status IN ('receiving', 'complete', 'aborted')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- "One unfinished upload per table" — the same guarantee
-- game_uploads_one_receiving_idx gives a whole dump, at the finer grain of
-- one table, so a second browser tab starting a fresh upload for a table
-- that is already receiving one races PostgreSQL's own uniqueness rather
-- than a check-then-insert in application code (CLAUDE.md rule 7).
-- lower(table_name) is what makes this agree with Definition.Validate's own
-- folded uniqueness check on table names.
CREATE UNIQUE INDEX game_table_data_one_receiving_idx
    ON game_table_data (contest_id, lower(table_name)) WHERE status = 'receiving';

-- "One current file per table" — the row a build actually loads. A second,
-- 'receiving' upload for the same table is allowed to exist alongside it (the
-- displacement pattern CompleteUpload already uses for a whole dump): only
-- once that upload completes does it retire this one, in the same statement
-- that would otherwise violate this index.
CREATE UNIQUE INDEX game_table_data_one_complete_idx
    ON game_table_data (contest_id, lower(table_name)) WHERE status = 'complete';

-- What the abandoned-upload janitor scans (provisioning.Games.SweepUploads,
-- extended by this feature to sweep both kinds of file it now owns). Without
-- this partial index it walks every table-data row the installation has ever
-- seen, on every tick, forever.
CREATE INDEX game_table_data_receiving_updated_idx
    ON game_table_data (updated_at) WHERE status = 'receiving';
