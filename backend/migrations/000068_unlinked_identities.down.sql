-- Forgets every unlinked identity: each may link to its account by email
-- again.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS unlinked_identities;
