-- The game builder ticks every five seconds (internal/app/background.go), and
-- its claim reads `status = 'pending' OR (status = 'building' AND updated_at
-- < now() - interval)` ordered by updated_at. That comment claimed "one
-- indexed row read"; the plan was a sequential scan and a sort, seventeen
-- thousand times a day, and the only thing keeping it cheap was that
-- game_templates has one row per contest ever created (CLAUDE.md rule 7 — a
-- filter a job offers is backed by an index, in the same change).
--
-- Partial on the two statuses the claim looks for, which is the small
-- minority: 'ready' is where a template spends its whole life, and 'dropped'
-- accumulates without bound as the reclaim sweep works. So this index holds
-- almost nothing almost always, which is the property a growing table needs.
CREATE INDEX game_templates_buildable_idx ON game_templates (updated_at)
    WHERE status IN ('pending', 'building');
