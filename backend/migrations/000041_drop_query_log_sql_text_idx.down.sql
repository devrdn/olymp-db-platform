-- Rebuilt concurrently, alone in its file, as it is dropped.
CREATE INDEX CONCURRENTLY IF NOT EXISTS query_log_sql_text_idx
    ON query_log USING gin (to_tsvector('simple', sql_text));
