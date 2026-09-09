-- The one filter game_templates offers that nothing indexed.
--
-- provisioning.Games.sweepOrphanFiles asks, once per candidate file per tick,
-- whether any contest's game is still built from it — an EXISTS on
-- game_templates.upload_id (internal/postgres.UploadInUse). The table holds one
-- row per contest, so a sequential scan of it costs nothing anybody can
-- measure, and this index buys no wall-clock time today.
--
-- It lands anyway because CLAUDE.md rule 7 is a rule about the code and the
-- migration arriving together, not about the row count on the day they do: a
-- WHERE clause an endpoint or a sweep offers is backed by an index in the same
-- change, or the next person reading UploadInUse has to work out for themselves
-- whether the omission was reasoned or forgotten. Partial, because the column is
-- NULL for every editor- and builder-sourced game and those rows are never what
-- the lookup is for.
CREATE INDEX game_templates_upload_id_idx
    ON game_templates (upload_id) WHERE upload_id IS NOT NULL;
