DROP INDEX IF EXISTS game_instances_active_idx;

-- Rolling back cannot leave a row the narrower constraint forbids. 'pending'
-- reads truer than 'failed' for a template whose database is simply gone —
-- it needs building again, not that a build attempt errored — and Live's own
-- WHERE t.status = 'ready' (postgres/gameinstances.go) already treats
-- anything but 'ready' as "no pool to tend", so this changes no live
-- contest's behaviour.
UPDATE game_templates SET status = 'pending' WHERE status = 'dropped';

ALTER TABLE game_templates DROP CONSTRAINT game_templates_status_check;
ALTER TABLE game_templates ADD CONSTRAINT game_templates_status_check
    CHECK (status IN ('pending', 'building', 'ready', 'failed'));
