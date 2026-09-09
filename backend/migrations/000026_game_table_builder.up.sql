-- A third way to build a contest's game (feat/game-table-builder), alongside
-- the editor (migration 3, source = 'editor') and a finished-dump upload
-- (migration 24, source = 'file'): an organiser describes tables, columns
-- and a primary key structurally instead of writing SQL. Turning that
-- description into the SQL that actually builds the game is a later task's
-- own work — this migration only gives it a place to be saved.
--
-- One jsonb column, not a table of its own. The definition is small — a
-- handful of tables, bounded well under a hundred kilobytes
-- (internal/provisioning.MaxDefinitionBytes) — and it belongs beside
-- init_script and upload_id for the same reason those do: it is one more
-- way of saying what this row's own game is, not a separate entity that
-- would need its own foreign key back to a game_templates row it can only
-- ever have one of.
ALTER TABLE game_templates
    ADD COLUMN definition_json jsonb;

-- Widens the closed list migration 24 gave `source`. Dropped and re-added
-- under its original name (`game_templates_source_check`, PostgreSQL's own
-- default for an unnamed column CHECK) rather than left as a second
-- constraint beside it, so there is exactly one place that says what
-- `source` may be.
ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_source_check,
    ADD CONSTRAINT game_templates_source_check
        CHECK (source IN ('editor', 'file', 'builder'));

-- Pairs 'builder' with definition_json the same way migration 24 paired
-- 'file' with upload_id: a builder-sourced row must carry a definition, and
-- nothing else may carry one left over from a source it was replaced from
-- (SaveDefinition and SaveScript both clear the column the other source
-- does not use, in the same upsert that writes the new one).
ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_source_pairing,
    ADD CONSTRAINT game_templates_source_pairing
        CHECK (
            (source = 'file') = (upload_id IS NOT NULL)
            AND (source = 'builder') = (definition_json IS NOT NULL)
        );
