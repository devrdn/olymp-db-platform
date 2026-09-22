-- The live feed's range over a whole contest's answers, the same shape as
-- query_log_contest_executed_idx. No INCLUDE columns: the feed shows the
-- answer itself — its value, its question and its points — so the row is read
-- either way.
--
-- Alone in its file, and concurrently, for the reason
-- 000035_query_log_contest_time_idx.up.sql gives.
CREATE INDEX CONCURRENTLY IF NOT EXISTS submissions_contest_submitted_idx
    ON submissions (contest_id, submitted_at, id);
