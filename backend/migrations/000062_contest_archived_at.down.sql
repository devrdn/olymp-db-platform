-- Discards every contest's archive time; it is not kept elsewhere.
SET lock_timeout = '5s';

DROP TRIGGER IF EXISTS contests_stamp_archived_insert ON contests;
DROP TRIGGER IF EXISTS contests_stamp_archived ON contests;
DROP FUNCTION IF EXISTS contests_stamp_archived();
ALTER TABLE contests DROP COLUMN IF EXISTS archived_at;
