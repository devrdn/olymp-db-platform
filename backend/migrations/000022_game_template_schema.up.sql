-- The console's schema panel (docs/design/preview.html, "SQL-консоль") shows
-- the participant the tables, their columns and the foreign keys between
-- them. That is a fact about the game, not about any one participant's copy:
-- every instance of a contest is cloned from the same template, so the answer
-- is identical for all of them and is worth working out once.
--
-- Worked out from a real database rather than from init_script, which is
-- already on this table: the script is the DDL somebody wrote, and the
-- catalogue is what the database actually built from it. Only the second one
-- is the truth the participant is being shown.
--
-- Not read from the template database, though. Connecting to it is exactly
-- what makes `CREATE DATABASE ... TEMPLATE` fail (SQLSTATE 55006, "source
-- database is being accessed by other users"), so a schema read against the
-- template would intermittently break provisioning for everybody. It is read
-- from the first instance that asks and cached here.
ALTER TABLE game_templates
    ADD COLUMN schema_json    jsonb,
    -- Which template version the cached document describes. A rebuild bumps
    -- version, so a stale cache is detected by comparing two columns of the
    -- row already being read — there is no separate invalidation step to
    -- forget to run, and no window where a rebuilt game serves the old
    -- shape.
    ADD COLUMN schema_version int,
    ADD CONSTRAINT game_templates_schema_pairing
        CHECK ((schema_json IS NULL) = (schema_version IS NULL));
