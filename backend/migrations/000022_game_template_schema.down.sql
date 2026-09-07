-- The cache is derived data: dropping it costs one catalogue read per
-- template the next time a participant opens the console, and nothing else.
ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_schema_pairing,
    DROP COLUMN schema_version,
    DROP COLUMN schema_json;
