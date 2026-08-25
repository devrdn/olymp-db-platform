-- Two additions: how many questions a contest asks, and translating the
-- authored content around them.
--
-- The game database itself stays English — schema, suspects, evidence, all of
-- it. That is what keeps this migration small: participants query one shared
-- English dataset whatever language they read the story in, so there are no
-- per-language templates, no per-language provisioning, and no need to pin a
-- participant to a language for the lifetime of their instance.

-- --- Question modes ---------------------------------------------------------

ALTER TABLE contests
    -- 'multi'  — several questions, each scored (the default).
    -- 'single' — one question carrying the whole contest, which is the shape a
    --            classic "here is the story, name the culprit" olympiad takes.
    ADD COLUMN question_mode text NOT NULL DEFAULT 'multi'
        CHECK (question_mode IN ('multi', 'single'));

ALTER TABLE questions
    -- Whether the participant is shown the question text at all. A hidden
    -- question still exists with its reference answers and points; the story is
    -- simply expected to make the task obvious, and the participant sees only
    -- an answer field. Working out what is being asked is then part of the
    -- puzzle rather than a line of instructions.
    --
    -- Per question rather than per contest: the single-question contest is the
    -- case that motivated it, but nothing about hiding is specific to that
    -- mode, and a contest-level flag would have to be re-invented the first
    -- time somebody wants one hidden question among several.
    ADD COLUMN is_visible boolean NOT NULL DEFAULT true;

-- "Exactly one question when question_mode = 'single'" is deliberately NOT a
-- constraint or trigger. An organizer building such a contest passes through
-- zero questions and, while replacing one, through two; a trigger would fight
-- the editor for no gain, since the invariant only has to hold at publish. It
-- is enforced by the publish gate instead, alongside translation completeness.

-- --- Languages --------------------------------------------------------------

-- Adding a language is an INSERT, never a migration or a deploy. That is the
-- whole reason this is a table and not an enum or a Go constant.
CREATE TABLE languages (
    code        text PRIMARY KEY,          -- BCP-47: 'en', 'ru', 'kk', 'ru-KZ'
    name        text NOT NULL,             -- English name, for admin screens
    native_name text NOT NULL,             -- endonym, for the language picker
    is_active   boolean NOT NULL DEFAULT true,
    sort_order  int NOT NULL DEFAULT 100
);

-- The three the platform launches with. A fourth is one INSERT away: nothing
-- in the schema, the Go code or the deployment enumerates these codes.
INSERT INTO languages (code, name, native_name, sort_order) VALUES
    ('en', 'English',  'English',  10),
    ('ro', 'Romanian', 'Română',   20),
    ('ru', 'Russian',  'Русский',  30);

-- Which languages a contest is offered in, and which one answers when the
-- participant asks for something else.
CREATE TABLE contest_languages (
    contest_id uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    lang       text NOT NULL REFERENCES languages ON DELETE RESTRICT,
    is_default boolean NOT NULL DEFAULT false,
    PRIMARY KEY (contest_id, lang)
);

-- Exactly one fallback per contest: "which language do we serve when the
-- requested one is missing" must never be ambiguous.
CREATE UNIQUE INDEX contest_languages_single_default_idx
    ON contest_languages (contest_id) WHERE is_default;

-- --- Translated content -----------------------------------------------------

-- Side tables rather than jsonb columns on the base rows. jsonb would drop the
-- foreign key on the language code (a typo'd 'rus' would be accepted in
-- silence), lose per-field NOT NULL, and turn "which contests are missing an
-- English story" — the query the publish gate is built on — into key
-- gymnastics. It also keeps the reference-answer relationship honest: answers
-- hang off the question, not off any one of its translations.

CREATE TABLE contest_translations (
    contest_id  uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    lang        text NOT NULL REFERENCES languages ON DELETE RESTRICT,
    title       text NOT NULL,
    description text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contest_id, lang)
);

CREATE TABLE story_translations (
    story_id   uuid NOT NULL REFERENCES stories ON DELETE CASCADE,
    lang       text NOT NULL REFERENCES languages ON DELETE RESTRICT,
    body_md    text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (story_id, lang)
);

CREATE TABLE question_translations (
    question_id uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    lang        text NOT NULL REFERENCES languages ON DELETE RESTRICT,
    body_md     text NOT NULL,
    -- Labels for a choice question, keyed by the identifiers in
    -- questions.choice_ids: {"a": "The butler", "b": "The gardener"}.
    choices     jsonb,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (question_id, lang)
);

-- The authored text moves into those tables outright. Keeping a copy on the
-- base row as "the default language" would be two sources of truth for one
-- fact, which is the failure this codebase keeps designing out elsewhere.
-- Nothing writes contests yet, so there is no data to carry across.
ALTER TABLE contests DROP COLUMN title, DROP COLUMN description;
ALTER TABLE stories DROP COLUMN body_md;
ALTER TABLE questions DROP COLUMN body_md, DROP COLUMN choices;

-- Stable, language-independent identifiers for the options of a choice
-- question. The submitted answer is one of these ids, never a label, so
-- grading a choice question does not depend on the language it was read in.
ALTER TABLE questions ADD COLUMN choice_ids text[] NOT NULL DEFAULT '{}';

-- Reference answers get no language column on purpose. The game database is
-- English, so the answer a participant reads out of it is English whichever
-- language the story was in. Where a translated story transliterates a name,
-- the accepted spelling is simply another row — the table already allows
-- several answers per question, and matching stays language-agnostic: a
-- participant who identified the culprit has solved the puzzle, and refusing
-- their spelling would score language rather than detection.

-- --- Participant preference -------------------------------------------------

ALTER TABLE users
    -- Preferred interface language. NULL means "no preference recorded" and
    -- resolution falls through to the request's Accept-Language.
    --
    -- Deliberately not pinned per registration: the game instance is language
    -- neutral, so a participant may switch language mid-contest and lose
    -- nothing. Pinning would only be needed if their database differed by
    -- language, which is exactly the design this migration avoids.
    ADD COLUMN locale text REFERENCES languages ON DELETE SET NULL;
