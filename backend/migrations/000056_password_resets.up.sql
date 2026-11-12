-- Password reset links waiting to be followed (internal/recovery): one per
-- account, a new request replacing the old, and only the token's SHA-256 is
-- kept. An expired row opens nothing and is swept.
SET lock_timeout = '5s';

CREATE TABLE password_resets (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- The lowercased address the link was mailed to: following it proves
    -- that address and no other the account has since.
    email      text NOT NULL CHECK (char_length(email) <= 254),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One link per account: a new request replaces the old one through it.
CREATE UNIQUE INDEX password_resets_user_id_key ON password_resets (user_id);
CREATE INDEX password_resets_expires_at_idx ON password_resets (expires_at);
