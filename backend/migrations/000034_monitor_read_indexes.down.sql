DROP INDEX IF EXISTS audit_log_failed_login_idx;

CREATE INDEX IF NOT EXISTS participant_events_registration_idx ON participant_events (registration_id, id);
CREATE INDEX IF NOT EXISTS participant_events_contest_idx ON participant_events (contest_id, id);
DROP INDEX IF EXISTS participant_events_registration_time_idx;
DROP INDEX IF EXISTS participant_events_contest_time_idx;
