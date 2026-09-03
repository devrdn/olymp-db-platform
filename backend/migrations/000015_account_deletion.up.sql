-- Deletion is a third status rather than a deleted_at column.
--
-- Sign-in (internal/auth/service.go) and the auth middleware both already
-- refuse anything that is not `active`, and CountActiveWithRole counts by
-- status. A third status closes every one of those doors at once; a separate
-- column would need each of them edited by hand, and one forgotten place
-- means a deleted account still works.

ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('active', 'blocked', 'deleted'));

-- What explains the current status. One set of columns for blocking and for
-- deletion, because what is being explained is the status.
ALTER TABLE users
    ADD COLUMN status_reason     text,
    ADD COLUMN status_changed_at timestamptz,
    ADD COLUMN status_changed_by uuid REFERENCES users;

-- A deleted account must not hold its login hostage: the common reason to
-- delete one is that it was created wrongly, and the next thing the
-- administrator does is create it again properly.
--
-- `login` was declared UNIQUE inline in 000001, which also guards the exact
-- (case-sensitive) value; that plain constraint has to go too, or a deleted
-- account still blocks the same login from being reused, just without the
-- case-insensitive check that used to also be reported through it.
ALTER TABLE users DROP CONSTRAINT users_login_key;

DROP INDEX users_login_lower_key;
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login))
    WHERE status <> 'deleted';

ALTER TABLE users DROP CONSTRAINT users_email_key;
CREATE UNIQUE INDEX users_email_key ON users (email)
    WHERE status <> 'deleted';

-- Every listing now filters on status, including the default one that hides
-- deleted accounts.
CREATE INDEX users_status_idx ON users (status);
