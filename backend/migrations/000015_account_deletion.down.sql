-- The up migration exists precisely so a deleted account releases its login
-- and email for reuse; the first realistic use of that (delete "ivanov",
-- create a new "ivanov") leaves two rows sharing the same login, one of them
-- deleted. Restoring the full-table UNIQUE constraints below would then fail
-- with a duplicate-key error, and it must not be worked around by deleting
-- the older row -- a rollback that destroys accounts is worse than the
-- problem it is rolling back.
--
-- So first make every login/email still held by a deleted account unique by
-- suffixing it with the row's id: the id is unique by definition, the suffix
-- keeps the original value readable, and only rows still `deleted` at
-- rollback time are touched -- a row that was itself later restored to
-- `active` or `blocked` already went through the ordinary path and kept its
-- real login. Concatenating NULL (an account with no email) stays NULL, so
-- the email side needs no separate guard.
UPDATE users
SET login = login || '-deleted-' || id::text,
    email = email || '-deleted-' || id::text
WHERE status = 'deleted';

DROP INDEX users_status_idx;

DROP INDEX users_email_key;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

DROP INDEX users_login_lower_key;
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login));
ALTER TABLE users ADD CONSTRAINT users_login_key UNIQUE (login);

ALTER TABLE users
    DROP COLUMN status_changed_by,
    DROP COLUMN status_changed_at,
    DROP COLUMN status_reason;

-- Rolling back cannot leave rows the constraint forbids.
UPDATE users SET status = 'blocked' WHERE status = 'deleted';
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('active', 'blocked'));
