-- Leaves the trail append-only by convention again, as before 000043.
SET lock_timeout = '5s';

DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
DROP FUNCTION IF EXISTS audit_log_refuse_change();
