-- Discards everything recorded about participants' activity; it is not
-- archived.
DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'contest.monitor');

DELETE FROM permissions WHERE code = 'contest.monitor';

DROP INDEX IF EXISTS audit_log_failed_login_idx;

-- The keyset indexes back to what migrations 000004 and 000030 made.
DROP INDEX IF EXISTS submissions_registration_submitted_idx;
CREATE INDEX submissions_registration_submitted_idx
    ON submissions (registration_id, submitted_at)
    INCLUDE (question_id, is_correct, points_awarded);
DROP INDEX IF EXISTS query_log_registration_executed_idx;
CREATE INDEX query_log_registration_executed_idx ON query_log (registration_id, executed_at DESC);

ALTER TABLE query_log
    DROP COLUMN IF EXISTS sql_fingerprint,
    DROP COLUMN IF EXISTS ip;

DROP TABLE IF EXISTS workspace_revisions;
DROP TABLE IF EXISTS participant_events;
