DROP INDEX IF EXISTS game_instances_active_idx;

-- Rolling back cannot leave a row the narrower constraint forbids. 'pending'
-- reads truer than 'failed' for a template whose database is simply gone —
-- it needs building again, not that a build attempt errored — and Live's own
-- WHERE t.status = 'ready' (postgres/gameinstances.go) already treats
-- anything but 'ready' as "no pool to tend", so this changes no live
-- contest's behaviour.
--
-- Be honest about what this restores, though: nothing in production ever
-- writes game_templates back to 'pending' and then actually builds it again
-- — the only writer of that table today is the contest-authoring flow that
-- created it in the first place, run once, before the contest ever
-- published. So a contest whose template this migration's own up.sql (or the
-- reclaim sweep, once it ran) had already marked 'dropped' comes out of this
-- rollback with a row that reads 'pending' but no code path that will ever
-- pick it up and rebuild the database it names. If this down migration runs
-- against an installation where the reclaim sweep has already reclaimed real
-- templates, those contests are left with no game and no route to get one
-- back short of hand-written SQL or a fresh contest — this statement changes
-- what the row says, not what the platform is able to do with it.
UPDATE game_templates SET status = 'pending' WHERE status = 'dropped';

ALTER TABLE game_templates DROP CONSTRAINT game_templates_status_check;
ALTER TABLE game_templates ADD CONSTRAINT game_templates_status_check
    CHECK (status IN ('pending', 'building', 'ready', 'failed'));
