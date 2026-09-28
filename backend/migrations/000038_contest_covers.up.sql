-- The picture above a contest, and who took it.
--
-- One row per olympiad, and nothing but the row: the files themselves live in
-- a directory on a volume (COVER_DIR, internal/platform/filestore), because a
-- 16/9 photograph in two sizes for every olympiad is not what the database is
-- for and not what a dump should have to carry. The design spec records that
-- decision and names its price — a second API replica has a different disk —
-- in docs/ARCHITECTURE.md §9.7.
--
-- A file is never removed by the statement that stops referring to it. The
-- cascade below drops the row when the contest goes, and the sweep that
-- already collects orphaned game databases collects the files: a file system
-- refusing a delete must not be able to fail the deletion of an olympiad.

-- Waiting is the dangerous half of a migration: DDL queued for a lock makes
-- every request needing the same table queue behind it. This file gives up
-- after five seconds rather than joining that queue — longer than any query
-- the API is allowed to run, so a wait past it is a wait on something else.
-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string), so this covers every statement below it.
SET lock_timeout = '5s';

CREATE TABLE contest_covers (
    -- The primary key as well as the foreign key: an olympiad has one cover
    -- or none, so there is no second row for a reader to choose between and
    -- no upload left behind by a replacement.
    contest_id  uuid PRIMARY KEY REFERENCES contests ON DELETE CASCADE,

    -- The SHA-256 of what was stored, which is also the file's own name
    -- (covers.Key: <hash>-1600.jpg). It names the output and never the
    -- upload, so the address changes exactly when the picture does — which is
    -- what lets the route serve it with a year of immutable caching.
    hash        text NOT NULL
        CHECK (hash ~ '^[0-9a-f]{64}$'),

    -- Whose picture it is. Mandatory for an uploaded cover (design spec
    -- §10.1) and bounded here as well as in the domain: the column is
    -- unbounded text and the request is bounded at eight mebibytes, so
    -- without this a credit line could be a megabyte of prose read back on
    -- every front page. The bound is covers.MaxAttributionLen; a drawn cover
    -- needs no attribution and has no row here at all.
    attribution text NOT NULL
        CHECK (char_length(attribution) BETWEEN 1 AND 200),

    -- The largest rendition's size — what was stored, not what was uploaded.
    width       int NOT NULL CHECK (width > 0),
    height      int NOT NULL CHECK (height > 0),

    uploaded_at timestamptz NOT NULL DEFAULT now(),
    -- Nullable, so the record outlives the account: the same promise every
    -- other uploaded_by in this schema makes.
    uploaded_by uuid REFERENCES users ON DELETE SET NULL
);

COMMENT ON TABLE contest_covers IS
    'One uploaded cover per contest; the files live on the volume named by COVER_DIR.';

-- No index beyond the primary key, deliberately. Every read is by contest_id,
-- which the primary key serves, and the only whole-table read is the sweep
-- looking for files nothing refers to — over one row per olympiad the
-- installation has ever run. CLAUDE.md rule 7 asks for an index behind a
-- filter the API offers, and this table offers none.
