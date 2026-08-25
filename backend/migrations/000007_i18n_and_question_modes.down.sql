-- Restores the single-language shape. The translated text is dropped with the
-- tables that hold it: rolling back a contest that was authored in three
-- languages cannot pick one to put back, so this is only safe on a schema that
-- has not been used yet — which is the only situation it exists for.

ALTER TABLE users DROP COLUMN locale;

ALTER TABLE questions DROP COLUMN choice_ids;
ALTER TABLE questions ADD COLUMN body_md text NOT NULL DEFAULT '';
ALTER TABLE questions ADD COLUMN choices jsonb;
ALTER TABLE questions ALTER COLUMN body_md DROP DEFAULT;

ALTER TABLE stories ADD COLUMN body_md text NOT NULL DEFAULT '';
ALTER TABLE stories ALTER COLUMN body_md DROP DEFAULT;

ALTER TABLE contests ADD COLUMN title text NOT NULL DEFAULT '';
ALTER TABLE contests ADD COLUMN description text;
ALTER TABLE contests ALTER COLUMN title DROP DEFAULT;

DROP TABLE IF EXISTS question_translations;
DROP TABLE IF EXISTS story_translations;
DROP TABLE IF EXISTS contest_translations;
DROP TABLE IF EXISTS contest_languages;
DROP TABLE IF EXISTS languages;

ALTER TABLE questions DROP COLUMN is_visible;
ALTER TABLE contests DROP COLUMN question_mode;
