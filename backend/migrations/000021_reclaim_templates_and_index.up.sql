-- §2.4's reclaim sweep (docs/ARCHITECTURE.md) now drops a contest's template
-- once every instance copied from it is gone, not only the instances
-- themselves — the template was the largest single database a contest owned
-- and nothing ever removed it. It needs the same terminal status
-- game_instances has carried since migration 3, for the same reason: an
-- organizer's audit search still has to have a row to find once the
-- database itself is gone from the cluster.
ALTER TABLE game_templates DROP CONSTRAINT game_templates_status_check;
ALTER TABLE game_templates ADD CONSTRAINT game_templates_status_check
    CHECK (status IN ('pending', 'building', 'ready', 'failed', 'dropped'));

-- The reclaim sweep's own query (internal/postgres/gameinstances.go,
-- Reclaimable and ReclaimableTemplates) filters game_instances on
-- `status <> 'dropped'`, joined from a finished or archived contest. That
-- predicate is not what game_instances_status_idx (migration 3, a plain
-- index on status) serves well: an inequality against one value out of four
-- touches most of the index, and 'dropped' is exactly the status that grows
-- without bound as the sweep does its job — every reclaimed instance adds to
-- the very majority this index would otherwise have to skip past on every
-- tick (CLAUDE.md rule 7: a filter the API — here, the background job —
-- offers is backed by an index, in the same change).
--
-- Partial on the opposite condition instead: an index that only ever
-- contains the still-active minority stays small for exactly as long as the
-- sweep keeps working, which is the property a growing-forever table needs.
CREATE INDEX game_instances_active_idx ON game_instances (contest_id)
    WHERE status <> 'dropped';
