-- Google and GitHub join Microsoft as providers an account can sign in
-- through; the list matches users.Providers.
SET lock_timeout = '5s';

ALTER TABLE external_identities DROP CONSTRAINT external_identities_provider_check;
ALTER TABLE external_identities
    ADD CONSTRAINT external_identities_provider_check
    CHECK (provider IN ('microsoft', 'google', 'github'));
