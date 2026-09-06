-- The background scheduler (docs/ARCHITECTURE.md §8, contests.Scheduler)
-- moves every running contest whose ends_at has passed to finished, on a
-- tick short enough that a dead replica only delays the visible status by a
-- moment. Its WHERE clause filters on (status, ends_at) — the mirror of the
-- (status, starts_at) pair contests_status_starts_at_idx already serves for
-- the published → running half of the same scheduler. Without this index,
-- every tick is a sequential scan of the whole table for the running half:
-- tolerable today, and exactly the "filter with no index" CLAUDE.md rule 7
-- exists to catch before this installation is running a contest on it.
CREATE INDEX contests_status_ends_at_idx ON contests (status, ends_at);
