ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_upload_id_fkey,
    DROP CONSTRAINT game_templates_source_pairing,
    DROP COLUMN upload_id,
    DROP COLUMN source;

-- Both indexes on it go with it.
DROP TABLE game_uploads;
