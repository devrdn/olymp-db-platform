-- Watching a participant
-- (docs/superpowers/specs/2026-09-18-participant-monitoring-design.md): what a
-- participant did that no other table records, the history of their notes and
-- SQL tabs, where each of their queries came from, and the permission that
-- lets a contest's staff see all of it.

-- Signals from the participant's browser (leaving the page, pasting) and from
-- the server (a new address, a second session, a tab's life). Append-only.
-- contest_id is denormalised on purpose: the live feed of a whole contest
-- reads a range of its own index past a cursor with no join to
-- registrations. Every payload field is bounded by internal/monitor before it
-- reaches this table; client_at is only what a browser claimed and is set for
-- browser signals alone.
--
-- The contest is named twice, directly and through the registration, and the
-- composite foreign key holds the two to agree (registrations_id_contest_key,
-- migration 000012): a caller's mix-up can never file one participant's
-- pastes and addresses in another contest's feed. The same key carries the
-- cascade from registrations, and through them from contests.
CREATE TABLE participant_events (
    id              bigserial PRIMARY KEY,
    contest_id      uuid NOT NULL,
    registration_id uuid NOT NULL,
    kind            text NOT NULL,
    payload         jsonb NOT NULL DEFAULT '{}',
    client_at       timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT participant_events_registration_contest_fkey
        FOREIGN KEY (registration_id, contest_id)
        REFERENCES registrations (id, contest_id) ON DELETE CASCADE
);

-- The feed is ordered by time, not by id: it is merged with the query log,
-- the answers and the sign-ins, which share no ids with this table. A page
-- after a cursor and the organiser's from/until are ranges of time, so each
-- index leads with its scope and then the time, with the id last as the
-- keyset's tiebreak.
-- One participant's timeline, and the cascade from registrations.
CREATE INDEX participant_events_registration_time_idx
    ON participant_events (registration_id, created_at, id);
-- The live feed of a whole contest.
CREATE INDEX participant_events_contest_time_idx
    ON participant_events (contest_id, created_at, id);

-- The history of a participant's notes and SQL tabs. document is 'notes' or a
-- tab's id; it is not a foreign key, so a deleted tab keeps its history.
-- A revision spans started_at..updated_at: a save within 30 seconds of the
-- revision's start rewrites it in place rather than adding one (see
-- internal/postgres/monitor.go).
CREATE TABLE workspace_revisions (
    id              bigserial PRIMARY KEY,
    registration_id uuid NOT NULL REFERENCES registrations ON DELETE CASCADE,
    document        text NOT NULL,
    title           text,
    body            text NOT NULL,
    started_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

-- One document's history in order, the latest revision of it, and the
-- cascade from registrations.
CREATE INDEX workspace_revisions_document_idx ON workspace_revisions (registration_id, document, id);

-- Where each query came from, and a hash of its text with case and spacing
-- normalised away (internal/monitor.Fingerprint), both written by the same
-- insert that journals the query. Rows written before this migration keep
-- NULL in both.
ALTER TABLE query_log
    ADD COLUMN ip inet,
    ADD COLUMN sql_fingerprint bigint;

-- "The same query as another participant": fingerprints per registration.
CREATE INDEX query_log_registration_fingerprint_idx ON query_log (registration_id, sql_fingerprint);

-- The organiser's pages of one participant's queries and answers are keyset
-- pages on (time, id): the feed, the timeline, the queries tab, and the
-- export, which reads every participant's range a page at a time. With the
-- id outside the index, each page sorts what it fetched, and a bitmap plan —
-- which any turn of the statistics can produce — fetches the rest of the
-- registration's range for every page, a read that grows with its square.
-- With the whole keyset in the index the page is an ordered index scan that
-- stops at its LIMIT.
--
-- Replaced rather than added, under the same names, so neither table takes
-- another index write. Every other reader keeps what it had: the
-- participant's own log (History) reads (executed_at DESC, id DESC), which
-- this index serves backwards exactly; its CSV and the answers window read
-- by executed_at, a prefix; the counts and the leaderboard read the
-- registration's range, and the answers' INCLUDE columns stay.
DROP INDEX query_log_registration_executed_idx;
CREATE INDEX query_log_registration_executed_idx ON query_log (registration_id, executed_at, id);
DROP INDEX submissions_registration_submitted_idx;
CREATE INDEX submissions_registration_submitted_idx
    ON submissions (registration_id, submitted_at, id)
    INCLUDE (question_id, is_correct, points_awarded);

-- A participant's failed sign-ins, for the organiser's feed. A failed
-- sign-in has no actor — the account was not proven — and names only the
-- login that was typed (internal/auth recordFailure), so that is the only key
-- it can be found by. Partial, so it costs nothing on the rest of the trail;
-- lower(), because sign-in matches logins without case.
CREATE INDEX audit_log_failed_login_idx
    ON audit_log (lower(payload->>'login'), created_at)
    WHERE action = 'auth.login_failed';

INSERT INTO permissions (code, name) VALUES
    ('contest.monitor', 'Watch what participants of a contest did');

-- Whoever may look at a contest as staff may watch its participants.
INSERT INTO role_permissions (role_id, permission_id)
SELECT rp.role_id, monitor.id
FROM role_permissions rp
JOIN permissions view ON view.id = rp.permission_id AND view.code = 'contest.view'
CROSS JOIN permissions monitor
WHERE monitor.code = 'contest.monitor'
ON CONFLICT DO NOTHING;
