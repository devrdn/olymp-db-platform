-- The table descriptions: one markdown document per language that the
-- organiser writes about the game's tables, shown to participants on a tab of
-- their own. A table of its own rather than a column beside the story: the
-- document is optional and may exist in a language the story is not yet
-- written in, and a contest with none has no row at all.
--
-- The bound repeats the domain's (contests.maxSchemaNotesRunes) for a row
-- written past it. The foreign keys take a lock on contests and languages
-- that blocks writes to them; a longer wait for that lock gives up rather
-- than queue every change to a contest behind it.
SET lock_timeout = '5s';

CREATE TABLE contest_schema_notes (
    contest_id uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    lang       text NOT NULL REFERENCES languages ON DELETE RESTRICT,
    body_md    text NOT NULL
        CONSTRAINT contest_schema_notes_body_length CHECK (char_length(body_md) <= 100000),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contest_id, lang)
);
