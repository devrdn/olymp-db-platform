-- Gives the journals back the shape they had: the contest is reachable only
-- through the registration again.
DROP TRIGGER IF EXISTS submissions_contest_fill ON submissions;
DROP TRIGGER IF EXISTS query_log_contest_fill ON query_log;
DROP FUNCTION IF EXISTS monitoring_fill_contest();

ALTER TABLE submissions DROP COLUMN IF EXISTS contest_id;
ALTER TABLE query_log DROP COLUMN IF EXISTS contest_id;
