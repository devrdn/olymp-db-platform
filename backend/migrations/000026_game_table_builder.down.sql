ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_source_pairing,
    ADD CONSTRAINT game_templates_source_pairing
        CHECK ((source = 'file') = (upload_id IS NOT NULL));

ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_source_check,
    ADD CONSTRAINT game_templates_source_check
        CHECK (source IN ('editor', 'file'));

ALTER TABLE game_templates
    DROP COLUMN definition_json;
