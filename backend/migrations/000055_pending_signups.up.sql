-- Sign-ups waiting for the code mailed to their address (internal/signup):
-- the account is made only once the code is confirmed. An expired row holds
-- nothing and is swept.
--
-- Every bound is the domain's; the checks keep a write that bypasses it from
-- storing what it would refuse.
SET lock_timeout = '5s';

CREATE TABLE pending_signups (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE CHECK (email = lower(email) AND char_length(email) <= 254),
    login         text NOT NULL CHECK (char_length(login) <= 100),
    full_name     text NOT NULL CHECK (char_length(full_name) <= 200),
    password_hash text NOT NULL,
    -- A random salt followed by SHA-256 of the salt and the code.
    code_hash     bytea NOT NULL,
    tries         smallint NOT NULL DEFAULT 0,
    sent_at       timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- A login is held by one sign-up at a time, whatever its case.
CREATE UNIQUE INDEX pending_signups_login_lower_key ON pending_signups (lower(login));
-- The cap's count and the sweep read unexpired and expired rows by it.
CREATE INDEX pending_signups_expires_at_idx ON pending_signups (expires_at);
