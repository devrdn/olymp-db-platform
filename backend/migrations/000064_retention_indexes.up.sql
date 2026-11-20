-- Indexes for the retention sweep, whose queries are in
-- internal/postgres/retention.go and filter two tables by columns nothing
-- else filters on:
--   contests: archived_at, to find the contests whose query journal has aged out;
--   mail_outbox: the moment a message finished, sent or abandoned.
-- query_log (query_log_contest_executed_idx) and audit_log
-- (audit_log_created_at_idx) already carry an index for their filters.
-- Both tables are small, so a plain CREATE INDEX is quick.
SET lock_timeout = '5s';

CREATE INDEX contests_archived_at_idx ON contests (archived_at)
    WHERE archived_at IS NOT NULL;

CREATE INDEX mail_outbox_finished_idx ON mail_outbox ((coalesce(sent_at, abandoned_at)))
    WHERE sent_at IS NOT NULL OR abandoned_at IS NOT NULL;
