DROP INDEX IF EXISTS users_email_trgm_idx;
DROP INDEX IF EXISTS users_full_name_trgm_idx;
DROP INDEX IF EXISTS users_login_trgm_idx;
DROP EXTENSION IF EXISTS pg_trgm;
