SET lock_timeout = '5s';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE password_hash IS NULL) THEN
        RAISE EXCEPTION 'accounts without a password exist; give them one before rolling back';
    END IF;
END $$;

DROP TABLE external_identities;

DROP INDEX users_email_lower_key;
CREATE UNIQUE INDEX users_email_key ON users (email)
    WHERE status <> 'deleted';

ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
ALTER TABLE users DROP COLUMN email_verified_at;
