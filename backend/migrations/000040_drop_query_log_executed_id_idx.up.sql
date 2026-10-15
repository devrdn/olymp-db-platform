-- Nothing reads query_log across all contests by time alone: every journal
-- read is scoped by registration or contest, and those have their own
-- indexes. Each participant query still paid for this one twice, on the
-- insert and on the completing update.
--
-- Alone in its file, and concurrently, for the reason
-- 000035_query_log_contest_time_idx.up.sql gives.
DROP INDEX CONCURRENTLY IF EXISTS query_log_executed_id_idx;
