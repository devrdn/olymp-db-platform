-- A count of the writes to a contest row, which a write decided against one
-- read is conditioned on (docs/ARCHITECTURE.md section 7.3). The trigger moves
-- it on every UPDATE, so no writer has to remember to.

SET lock_timeout = '5s';

ALTER TABLE contests ADD COLUMN version bigint NOT NULL DEFAULT 0;

CREATE FUNCTION contests_bump_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION contests_bump_version() IS
    'Moves contests.version on every write of the row.';

CREATE TRIGGER contests_bump_version
    BEFORE UPDATE ON contests
    FOR EACH ROW
    EXECUTE FUNCTION contests_bump_version();
