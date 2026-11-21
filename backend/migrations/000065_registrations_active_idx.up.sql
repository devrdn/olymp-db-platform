-- The count of active participants (internal/postgres/registrations.go,
-- CountActive), taken every thirty seconds: active registrations are a small
-- fraction of the table, which this index holds alone.
CREATE INDEX CONCURRENTLY IF NOT EXISTS registrations_active_idx
    ON registrations (contest_id) WHERE status = 'active';
