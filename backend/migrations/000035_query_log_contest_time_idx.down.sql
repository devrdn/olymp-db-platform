-- Dropped concurrently, and alone in its file for the same reason it was
-- built that way: a drop inside a transaction takes a lock that blocks every
-- reader of the journal.
DROP INDEX CONCURRENTLY IF EXISTS query_log_contest_executed_idx;
