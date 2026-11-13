SET lock_timeout = '5s';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM external_identities WHERE provider <> 'microsoft') THEN
        RAISE EXCEPTION 'identities of google or github exist; remove them before rolling back';
    END IF;
END
$$;

ALTER TABLE external_identities DROP CONSTRAINT external_identities_provider_check;
ALTER TABLE external_identities
    ADD CONSTRAINT external_identities_provider_check
    CHECK (provider IN ('microsoft'));
