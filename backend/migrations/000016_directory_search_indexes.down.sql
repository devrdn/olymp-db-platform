-- Removes what this migration added — the three trigram indexes — not the
-- extension they are built from. pg_trgm is an installation-wide extension,
-- not this migration's private property: another index, current or future,
-- may depend on it for its own reasons, and dropping it here would take
-- that dependency down as a side effect of rolling back a search feature it
-- has nothing to do with. CREATE EXTENSION IF NOT EXISTS in the up migration
-- is idempotent for the same reason — it is content to find the extension
-- already there.
DROP INDEX IF EXISTS users_email_trgm_idx;
DROP INDEX IF EXISTS users_full_name_trgm_idx;
DROP INDEX IF EXISTS users_login_trgm_idx;
