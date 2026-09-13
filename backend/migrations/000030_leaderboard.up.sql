-- The leaderboard (docs/superpowers/specs/2026-09-13-leaderboard-design.md).
--
-- How long before the window ends the table stops changing for everybody but
-- the contest's staff. NULL is "no freeze". Bounded in the schema as well as in
-- contests.Contest.Validate (CLAUDE.md rule 2): a week, the longest a contest
-- window can usefully be frozen for, matching the bound on duration_min.
ALTER TABLE contests
    ADD COLUMN leaderboard_freeze_min integer
        CHECK (leaderboard_freeze_min IS NULL OR leaderboard_freeze_min BETWEEN 1 AND 10080);

-- How a participant is labelled on a table somebody other than the staff can
-- read — and that table is public. 'login' by default, so a full name reaches
-- the page only when an organiser chose it.
ALTER TABLE contests
    ADD COLUMN leaderboard_names text NOT NULL DEFAULT 'login'
        CHECK (leaderboard_names IN ('login', 'full_name'));

-- When the organiser revealed a frozen table's final state. Written once and
-- never cleared: what has been seen cannot be unseen.
ALTER TABLE contests
    ADD COLUMN leaderboard_revealed_at timestamptz;

-- The standings are one aggregate over a registration's submissions cut off at
-- a moment (submitted_at < cutoff). The existing unique index leads with
-- registration_id but orders by question and attempt, so it cannot serve the
-- time cutoff; this one can, and it carries every column the aggregate reads
-- so the table itself is not visited (CLAUDE.md rule 7).
CREATE INDEX submissions_registration_submitted_idx
    ON submissions (registration_id, submitted_at)
    INCLUDE (question_id, is_correct, points_awarded);
