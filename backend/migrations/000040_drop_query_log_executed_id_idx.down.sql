-- Rebuilt concurrently, alone in its file, as it is dropped.
CREATE INDEX CONCURRENTLY IF NOT EXISTS query_log_executed_id_idx
    ON query_log (executed_at DESC, id DESC);
