-- A question gains a short title per language, beside its body. Optional: an
-- empty title is "none", so every existing question keeps working and is shown
-- as before, body first.
--
-- The bound repeats the domain's (contests.maxQuestionTitleRunes) for a row
-- written past it. Checking it reads every existing translation under the
-- exclusive lock, a few hundred rows at most; a longer wait for that lock
-- gives up rather than queue participants' question reads behind it.
SET lock_timeout = '5s';

ALTER TABLE question_translations
    ADD COLUMN title text NOT NULL DEFAULT ''
        CONSTRAINT question_translations_title_length CHECK (char_length(title) <= 200);
