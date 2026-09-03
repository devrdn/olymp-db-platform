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
