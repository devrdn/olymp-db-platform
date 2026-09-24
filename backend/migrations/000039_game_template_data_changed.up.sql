-- When a contest's table-builder data last changed, and NULL when it has not
-- changed since the build.
--
-- The data an organiser enters in the table builder arrives *after* the game
-- is built and cannot arrive before it: the build runs seconds after the
-- schema is saved, and a row may not be typed into a table that is not yet in
-- the saved schema. Nothing marked the built template out of date, so the
-- rows were stored, never loaded, and every participant copied an empty
-- database. This column is the mark, and internal/provisioning's RequestBuild
-- is what acts on it.
--
-- Not a status. The template built before the edit is still `ready` and still
-- usable — participants get the previous data — so this sits beside the
-- status rather than inside it, and every reader that does not care about
-- freshness goes on reading `status` alone.

-- Waiting is the dangerous half of a migration: DDL queued for a lock makes
-- every request needing the same table queue behind it. This file gives up
-- after five seconds rather than joining that queue — longer than any query
-- the API is allowed to run, so a wait past it is a wait on something else.
-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string), so this covers every statement below it.
SET lock_timeout = '5s';

ALTER TABLE game_templates ADD COLUMN data_changed_at timestamptz;

-- Every game that already exists was built before this column did, and the
-- data it holds — if it is a builder game at all — was entered after that
-- build and never loaded. Marking those rows is not cosmetic: it is the only
-- way the organiser of an olympiad prepared last week is told that the
-- database their participants will copy is empty.
UPDATE game_templates
SET data_changed_at = now()
WHERE source = 'builder'
  AND status = 'ready'
  AND EXISTS (
      SELECT 1 FROM game_table_data d
      WHERE d.contest_id = game_templates.contest_id AND d.status = 'complete'
  );
