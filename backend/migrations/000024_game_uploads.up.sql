-- An organiser's second way to build a contest's game: upload a finished SQL
-- dump instead of writing (or pasting) one in the editor. Migration 3 gave
-- game_templates one column for the script; this is where the two paths
-- become distinguishable rows rather than the same column meaning two
-- different things depending on who is asked.
--
-- The bytes of an upload never reach this database and never will — they
-- live on the API host's disk (internal/gamefile), one instance's own
-- volume. This table is the bookkeeping that lets an upload survive the
-- request that started it: begin, append, complete and abort all touch this
-- row rather than holding state in the process that happened to receive the
-- chunk.
ALTER TABLE game_templates
    ADD COLUMN source text NOT NULL DEFAULT 'editor' CHECK (source IN ('editor', 'file')),
    ADD COLUMN upload_id uuid,
    -- A file-sourced game names the upload it came from; an editor-authored
    -- one has nothing to name. Every row this migration finds is 'editor'
    -- with a script already on it, so the default above is what makes this
    -- an ordinary column addition rather than a backfill.
    ADD CONSTRAINT game_templates_source_pairing
        CHECK ((source = 'file') = (upload_id IS NOT NULL));

CREATE TABLE game_uploads (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id     uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    -- The name a human gave the file, kept for the organiser's own screen.
    -- Bounded (CLAUDE.md rule 2): it is free text out of a browser's file
    -- picker, and nothing about it is ever used as a path — internal/gamefile
    -- names its files after this row's own id, never after this column.
    filename       text NOT NULL CHECK (length(filename) BETWEEN 1 AND 255),
    -- What the browser announced the file would be, checked against what
    -- actually landed on disk when the upload is completed
    -- (internal/gamefile.Store.Complete).
    declared_bytes bigint NOT NULL CHECK (declared_bytes > 0),
    -- How much has actually landed on disk so far — zero while nothing has
    -- arrived, final once the upload completes. It is what tells a resumed
    -- browser where to continue from after a dropped connection.
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    -- sha256 and line_count are filled in only once Complete has run; both
    -- stay null for the whole time an upload is 'receiving'.
    sha256         text,
    line_count     bigint,
    status         text NOT NULL DEFAULT 'receiving'
        CHECK (status IN ('receiving', 'complete', 'aborted')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Added once the table exists, rather than inline on game_templates above,
-- because game_uploads did not exist yet at that point in this script.
ALTER TABLE game_templates
    ADD CONSTRAINT game_templates_upload_id_fkey
        FOREIGN KEY (upload_id) REFERENCES game_uploads ON DELETE SET NULL;

-- "One unfinished upload per contest" is a database guarantee, not a
-- check-then-insert in application code: a partial unique index is what lets
-- two concurrent begins for the same contest race PostgreSQL's own
-- uniqueness instead of racing the gap between a SELECT and an INSERT
-- (CLAUDE.md rule 7 — the filter the code offers is backed by an index in
-- the same change).
CREATE UNIQUE INDEX game_uploads_one_receiving_idx
    ON game_uploads (contest_id) WHERE status = 'receiving';

-- What the abandoned-upload janitor scans (internal/app/background.go): rows
-- still 'receiving' that have gone untouched past their grace period.
-- Without this partial index the janitor walks every upload the
-- installation has ever seen, on every tick, forever.
CREATE INDEX game_uploads_receiving_updated_idx
    ON game_uploads (updated_at) WHERE status = 'receiving';
