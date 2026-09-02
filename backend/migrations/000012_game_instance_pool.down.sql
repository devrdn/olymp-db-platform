DROP INDEX IF EXISTS game_instances_contest_version_idx;
DROP INDEX IF EXISTS game_instances_free_idx;

ALTER TABLE game_instances
    DROP CONSTRAINT IF EXISTS game_instances_registration_fkey,
    DROP CONSTRAINT IF EXISTS game_instances_contest_fkey;

ALTER TABLE registrations DROP CONSTRAINT IF EXISTS registrations_id_contest_key;

-- Unowned copies cannot be represented once the column is required again.
DELETE FROM game_instances WHERE registration_id IS NULL;

ALTER TABLE game_instances
    ADD CONSTRAINT game_instances_registration_id_fkey
        FOREIGN KEY (registration_id) REFERENCES registrations ON DELETE CASCADE;

ALTER TABLE game_instances ALTER COLUMN registration_id SET NOT NULL;
ALTER TABLE game_instances DROP COLUMN contest_id;
