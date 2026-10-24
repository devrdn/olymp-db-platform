-- Questions participants of a running contest put to its organisers, the
-- organisers' answers, and their announcements (internal/clarifications), with
-- the permission to answer them.
--
-- The new unique key on questions holds an exclusive lock while its index
-- builds; a longer wait for that lock gives up rather than queue participants'
-- question reads behind it.
SET lock_timeout = '5s';

-- What a clarification may name: a question of its own contest.
ALTER TABLE questions ADD CONSTRAINT questions_id_contest_key UNIQUE (id, contest_id);

-- One participant's question and, once given, its answer. The contest is named
-- twice, directly and through the registration, and the composite keys hold
-- the three to agree: a question can only ever be filed in its asker's contest
-- and only name a question of it. Every bound is internal/clarifications'.
--
-- posted_at is when the row last appeared in a participant's feed: asked,
-- then answered. An edit does not move it.
CREATE TABLE clarifications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id      uuid NOT NULL,
    registration_id uuid NOT NULL,
    question_id     uuid,
    body            text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000),
    created_at      timestamptz NOT NULL,
    answer          text CHECK (char_length(answer) <= 4000),
    declined        boolean NOT NULL DEFAULT false,
    answered_by     uuid REFERENCES users ON DELETE SET NULL,
    answered_at     timestamptz,
    edited_at       timestamptz,
    visibility      text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public')),
    posted_at       timestamptz GENERATED ALWAYS AS (coalesce(answered_at, created_at)) STORED,

    CONSTRAINT clarifications_registration_contest_fkey
        FOREIGN KEY (registration_id, contest_id)
        REFERENCES registrations (id, contest_id) ON DELETE CASCADE,
    CONSTRAINT clarifications_question_contest_fkey
        FOREIGN KEY (question_id, contest_id)
        REFERENCES questions (id, contest_id) ON DELETE SET NULL (question_id),
    -- An answer and its time come together, and only an answer is public,
    -- declined or edited.
    CONSTRAINT clarifications_answer_shape CHECK (
        (answer IS NULL) = (answered_at IS NULL)
        AND (answered_at IS NOT NULL OR (visibility = 'private' AND NOT declined AND edited_at IS NULL))
    )
);

-- One index per read the routes offer (CLAUDE.md rule 7), each a range in the
-- order its page reads, with the id as the keyset's tiebreak.
-- A participant's own questions in the feed, the count of their open ones,
-- and the cascade from registrations.
CREATE INDEX clarifications_registration_posted_idx
    ON clarifications (registration_id, posted_at, id);
-- Every public answer of a contest in the feed.
CREATE INDEX clarifications_public_posted_idx
    ON clarifications (contest_id, posted_at, id) WHERE visibility = 'public';
-- The staff queue's waiting questions, oldest first, and their count.
CREATE INDEX clarifications_open_idx
    ON clarifications (contest_id, created_at, id) WHERE answered_at IS NULL;
-- The staff queue's answered questions, most recently answered first, and
-- their count.
CREATE INDEX clarifications_answered_idx
    ON clarifications (contest_id, answered_at, id) WHERE answered_at IS NOT NULL;
-- Not a route's read: deleting a question sets its clarifications' question
-- to null, which would otherwise scan the table.
CREATE INDEX clarifications_question_idx
    ON clarifications (question_id) WHERE question_id IS NOT NULL;

-- A message from a contest's organisers to all its participants.
CREATE TABLE announcements (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    body       text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
    author     uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL
);

-- A contest's announcements, newest first, in the feed and on the staff page,
-- and the cascade from contests.
CREATE INDEX announcements_contest_time_idx ON announcements (contest_id, created_at, id);

INSERT INTO permissions (code, name) VALUES
    ('contest.clarify', 'Answer participants'' questions and post announcements');

-- Whoever may look at a contest as staff may answer it; within a contest the
-- owner and its managers hold it (internal/rbac).
INSERT INTO role_permissions (role_id, permission_id)
SELECT rp.role_id, clarify.id
FROM role_permissions rp
JOIN permissions view ON view.id = rp.permission_id AND view.code = 'contest.view'
CROSS JOIN permissions clarify
WHERE clarify.code = 'contest.clarify'
ON CONFLICT DO NOTHING;
