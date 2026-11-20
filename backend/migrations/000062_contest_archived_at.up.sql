-- When a contest was archived: the moment the retention periods for its
-- journals count from.
--
-- updated_at cannot serve: any later edit moves it, and a deletion driven by
-- it would keep shifting. The trigger stamps the column on the move to
-- 'archived' and never again, derived rather than supplied, so every path
-- that archives a contest stamps it without having to remember to. A direct
-- write of archived_at is still allowed; the trigger watches status only.
--
-- The backfill takes updated_at for contests archived before this
-- migration. That is at or after the real archive time, so it errs late:
-- a journal may be kept longer than its period, never deleted early.

SET lock_timeout = '5s';

ALTER TABLE contests ADD COLUMN archived_at timestamptz;

CREATE FUNCTION contests_stamp_archived() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.archived_at := now();
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION contests_stamp_archived() IS
    'Stamps contests.archived_at when a contest becomes archived.';

CREATE TRIGGER contests_stamp_archived
    BEFORE UPDATE OF status ON contests
    FOR EACH ROW
    WHEN (NEW.status = 'archived' AND OLD.status IS DISTINCT FROM 'archived')
    EXECUTE FUNCTION contests_stamp_archived();

CREATE TRIGGER contests_stamp_archived_insert
    BEFORE INSERT ON contests
    FOR EACH ROW
    WHEN (NEW.status = 'archived' AND NEW.archived_at IS NULL)
    EXECUTE FUNCTION contests_stamp_archived();

UPDATE contests SET archived_at = updated_at WHERE status = 'archived';
