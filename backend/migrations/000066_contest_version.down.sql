-- Discards every contest's write count; nothing else reads it.
SET lock_timeout = '5s';

DROP TRIGGER IF EXISTS contests_bump_version ON contests;
DROP FUNCTION IF EXISTS contests_bump_version();
ALTER TABLE contests DROP COLUMN IF EXISTS version;
