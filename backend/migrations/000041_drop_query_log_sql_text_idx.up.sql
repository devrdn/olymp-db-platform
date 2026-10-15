-- Nothing searches query_log by to_tsvector: the panel's search is a
-- substring match inside one registration's range. Each participant query
-- still paid a tsvector parse and a GIN insert twice, on the insert and on
-- the completing update, which changes status and so is never a HOT update.
--
-- Alone in its file, and concurrently, for the reason
-- 000035_query_log_contest_time_idx.up.sql gives.
DROP INDEX CONCURRENTLY IF EXISTS query_log_sql_text_idx;
