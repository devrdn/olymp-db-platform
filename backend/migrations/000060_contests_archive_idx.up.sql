-- The archive list (internal/postgres/archive.go): open contests that have
-- ended, newest end first. The predicate is the list's own, so the planner
-- can match it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS contests_archive_idx
    ON contests (ends_at DESC NULLS LAST, id DESC)
    WHERE visibility = 'open' AND status IN ('finished', 'archived');
