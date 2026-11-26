-- The external accounts an administrator unlinked from an account. While a
-- row stands, that external account is not linked to that account again by
-- its email; lifting it deletes the row. The unique key's leading user_id
-- serves both the account's listing and the check a link makes.
SET lock_timeout = '5s';

CREATE TABLE unlinked_identities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider    text NOT NULL CHECK (provider IN ('microsoft', 'google', 'github')),
    issuer      text NOT NULL CHECK (char_length(issuer) <= 255),
    subject     text NOT NULL CHECK (char_length(subject) <= 255),
    email       text CHECK (char_length(email) <= 254),
    unlinked_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider, issuer, subject)
);
