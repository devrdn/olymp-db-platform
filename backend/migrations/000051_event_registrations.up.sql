-- Sign-ups for one evening's event through the public form
-- (internal/registrations), each with the code shown at the entrance. The
-- table is temporary: a later release drops it with the form.
--
-- Every bound is internal/registrations'; the checks keep a write that
-- bypasses the domain from storing what it would refuse.
SET lock_timeout = '5s';

CREATE TABLE event_registrations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    first_name    text NOT NULL CHECK (char_length(first_name) BETWEEN 1 AND 100),
    last_name     text NOT NULL CHECK (char_length(last_name) BETWEEN 1 AND 100),
    email         text NOT NULL CHECK (octet_length(email) BETWEEN 3 AND 254),
    -- The email lowercased by the domain: one registration per address,
    -- whatever its case. Not checked against lower(email), whose result
    -- outside ASCII depends on the database's collation.
    email_key     text NOT NULL UNIQUE,
    code          text NOT NULL UNIQUE CHECK (code ~ '^[A-HJKMNP-Z2-9]{6}$'),
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- When the code was last mailed; the resend cooldown is measured from it.
    last_sent_at  timestamptz,
    checked_in_at timestamptz,
    -- The account made with the registration; null when the person already
    -- had one.
    user_id       uuid REFERENCES users ON DELETE SET NULL
);

-- The foreign key's: deleting an account would otherwise scan the table.
CREATE INDEX event_registrations_user_idx ON event_registrations (user_id)
    WHERE user_id IS NOT NULL;
