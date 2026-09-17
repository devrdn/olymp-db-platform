-- The participant's own workspace on the play screen
-- (docs/superpowers/specs/2026-09-17-play-workspace-design.md): free-text
-- notes and SQL editor tabs, kept per registration so a reload, a crashed
-- browser or another computer in the lab loses nothing.
--
-- Both hang off the registration and go with it (ON DELETE CASCADE): the
-- workspace belongs to one participant in one olympiad and needs no cleanup
-- of its own. The limits (20000 characters of notes, ten tabs, titles of
-- 1-40 characters, a tab body no longer than a query) are enforced by
-- internal/workspace before anything reaches these tables.
CREATE TABLE participant_notes (
    registration_id uuid PRIMARY KEY REFERENCES registrations ON DELETE CASCADE,
    body            text NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE participant_sql_tabs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registration_id uuid NOT NULL REFERENCES registrations ON DELETE CASCADE,
    position        int  NOT NULL,
    title           text NOT NULL,
    body            text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Every read of the tabs is one registration's, in position order; the same
-- index serves the cascade from registrations.
CREATE INDEX participant_sql_tabs_registration_idx ON participant_sql_tabs (registration_id, position);
