-- A contest has two archive properties: visibility, whether it appears in the
-- public archive and the lists once it has ended, and archive_tasks, whether
-- the archive shows its questions.
--
-- The CHECK mirrors the domain rule (contests.Contest.Validate): a contest
-- with self-registration is always open, so it cannot be closed. Every contest
-- that exists today becomes open with its tasks hidden. The column default
-- stays 'open' so the previous release's inserts still satisfy the CHECK; the
-- one write it can no longer make is switching a contest the new release
-- closed to self-registration, which now fails the CHECK.
SET lock_timeout = '5s';

ALTER TABLE contests
    ADD COLUMN visibility text NOT NULL DEFAULT 'open'
        CONSTRAINT contests_visibility_check CHECK (visibility IN ('open', 'closed')),
    ADD COLUMN archive_tasks boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT contests_self_registration_open
        CHECK (enrollment <> 'open' OR visibility = 'open');
