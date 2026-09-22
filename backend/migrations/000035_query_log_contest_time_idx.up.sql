-- The live feed's range over a whole contest's queries: the contest, then the
-- time, then the id as the keyset's tiebreak — the same shape
-- participant_events_contest_time_idx has, read forwards for a page after a
-- cursor and backwards for the newest page. The organiser's from/until are
-- bounds on the same time column, so they are part of the same range rather
-- than a filter over it (CLAUDE.md rule 7).
--
-- This file holds one statement and nothing else on purpose. cmd/migrate
-- hands a migration to PostgreSQL as one string, and a string of several
-- statements is executed as one implicit transaction — which CREATE INDEX
-- CONCURRENTLY refuses to run inside. A file with a single statement is not a
-- transaction block, so the build runs concurrently and never takes a lock
-- that blocks writers on a journal this size. Should a build fail it leaves
-- an invalid index behind: drop it by hand, then `migrate force` and run
-- again.
CREATE INDEX CONCURRENTLY IF NOT EXISTS query_log_contest_executed_idx
    ON query_log (contest_id, executed_at, id);
