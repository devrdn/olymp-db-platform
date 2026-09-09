-- Retires the table-data rows migration 27 left behind when a contest's game
-- stopped being the table builder's.
--
-- A row of game_table_data is addressed by (contest, table name) against the
-- contest's *current* definition. Saving a script in the editor, or
-- completing a dump upload, replaces that definition and leaves the rows
-- naming tables the game no longer has: nothing reads them, nothing checks
-- them, and — until the change this migration ships with — nothing deleted
-- them either. They were not merely dead. A definition saved afterwards that
-- happened to name the same table again inherited the same file, whose header
-- names columns and never their types, so values validated as text loaded
-- into a numeric column and a participant's database ended up holding data no
-- check had ever agreed to.
--
-- 'aborted' rather than DELETE, the status this schema already means "nobody
-- needs these bytes" by: the row stays as the history of a file that once
-- existed, the same convention game_instances keeps for a dropped database,
-- and provisioning's own orphan sweep (TableDataInUse) is what then removes
-- the file from the volume.
--
-- From here on provisioning.Games.replaceGame does this in the transaction
-- that replaces the game, so this statement is the one-off for installations
-- that already have such rows. Without it, those rows would meet the check
-- that now refuses a definition whose table still holds data — and an
-- organiser would be locked out of the table builder by data from a game they
-- replaced months ago.
UPDATE game_table_data d
SET status = 'aborted', updated_at = now()
WHERE d.status IN ('receiving', 'complete')
  AND NOT EXISTS (
      SELECT 1
      FROM game_templates t
      WHERE t.contest_id = d.contest_id
        AND t.source = 'builder'
  );
