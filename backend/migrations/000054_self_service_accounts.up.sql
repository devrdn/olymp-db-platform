-- Accounts that people create and sign in to by themselves: an email the
-- account has proved to own, no password for an account that signs in through
-- an external provider, and the provider identities themselves. Existing
-- emails were entered by an administrator, so they count as verified.
SET lock_timeout = '5s';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM users WHERE status <> 'deleted' AND email IS NOT NULL
        GROUP BY lower(email) HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'live accounts share an email that differs only in case; resolve them before migrating';
    END IF;
END $$;

ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

UPDATE users SET email_verified_at = now()
WHERE email IS NOT NULL AND status <> 'deleted';

DROP INDEX users_email_key;
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email))
    WHERE status <> 'deleted';

CREATE TABLE external_identities (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider     text NOT NULL CHECK (provider IN ('microsoft')),
    issuer       text NOT NULL CHECK (char_length(issuer) <= 255),
    subject      text NOT NULL CHECK (char_length(subject) <= 255),
    email        text CHECK (char_length(email) <= 254),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, issuer, subject)
);

CREATE INDEX external_identities_user_idx ON external_identities (user_id);
