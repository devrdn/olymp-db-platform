-- The account search behind internal/contests.Service.SearchPeople runs the
-- same leading-wildcard ILIKE across login, full_name and email that
-- internal/postgres/users.go's List already used — but List had one
-- audience (administrators, behind users.manage) and this reuses it behind
-- participant.manage, which every contest's staff holds. Nothing here was
-- ever backed by an index; a sequential scan of the whole table on every
-- keystroke a debounced picker still sends was tolerable for a screen a
-- handful of administrators opened and is not for one every contest's staff
-- can reach. CLAUDE.md's own rule 7 — a filter the API offers is backed by
-- an index, in the same change — is what this migration is for.
--
-- A plain btree cannot serve '%needle%': the wildcard on both sides means
-- there is no prefix to seek on. Trigram indexes can, because they match on
-- substrings of the value rather than the value's start.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX users_login_trgm_idx ON users USING gin (login gin_trgm_ops);
CREATE INDEX users_full_name_trgm_idx ON users USING gin (full_name gin_trgm_ops);
CREATE INDEX users_email_trgm_idx ON users USING gin (email gin_trgm_ops);
