-- Back to migration 24's own referential action. It is the one this migration
-- exists to remove, so rolling back deliberately restores the defect rather
-- than inventing a third behaviour: a down migration that left something else
-- behind would make "roll back to 24" mean two different schemas depending on
-- which way the installation got there.
ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_upload_id_fkey;

ALTER TABLE game_templates
    ADD CONSTRAINT game_templates_upload_id_fkey
        FOREIGN KEY (upload_id) REFERENCES game_uploads ON DELETE SET NULL;
