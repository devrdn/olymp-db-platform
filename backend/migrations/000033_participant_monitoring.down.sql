-- Discards everything recorded about participants' activity; it is not
-- archived.
DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'contest.monitor');

DELETE FROM permissions WHERE code = 'contest.monitor';

DROP INDEX IF EXISTS audit_log_failed_login_idx;

DROP INDEX IF EXISTS query_log_registration_fingerprint_idx;

ALTER TABLE query_log
    DROP COLUMN IF EXISTS sql_fingerprint,
    DROP COLUMN IF EXISTS ip;

DROP TABLE IF EXISTS workspace_revisions;
DROP TABLE IF EXISTS participant_events;
