-- The mark is derived state: it says the built database no longer matches the
-- data, and dropping it loses only the knowledge, never the data itself.
SET lock_timeout = '5s';

ALTER TABLE game_templates DROP COLUMN IF EXISTS data_changed_at;
