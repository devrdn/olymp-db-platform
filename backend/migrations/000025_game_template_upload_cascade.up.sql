-- Migration 24 gave game_templates.upload_id `ON DELETE SET NULL` and, in the
-- same statement group, a CHECK that pairs the two columns:
--
--     CHECK ((source = 'file') = (upload_id IS NOT NULL))
--
-- The two contradict each other. SET NULL clears upload_id and leaves
-- source = 'file', which is precisely the row the CHECK forbids, so deleting
-- a game_uploads row a file-sourced game names is refused outright:
--
--     new row for relation "game_templates" violates check constraint
--     "game_templates_source_pairing"
--
-- Deleting a contest happens to work today only because both tables cascade
-- from contests and PostgreSQL fires game_templates' own RI trigger first, by
-- creation order. That is correctness resting on OID order rather than on the
-- schema, invisible to whoever adds the next foreign key here, and it stops
-- being true the moment the constraints are recreated in another order — a
-- restore from a dump, for one.
--
-- CASCADE is the referential action that says what the CHECK already says: a
-- file-sourced game cannot exist without the upload it is built from, so an
-- upload that goes takes that game with it. Nothing in the service deletes a
-- game_uploads row at all — an abandoned or displaced upload is marked
-- 'aborted' and kept as history (provisioning.Games.abortUpload) — so the
-- only path this changes in practice is deleting a contest, where the game
-- row was going anyway.
--
-- SET NULL cannot be repaired instead by also flipping source back to
-- 'editor': a referential action can only write nulls or defaults, and a game
-- whose SQL was the deleted file is not an editor-authored game with an empty
-- script. It is nothing at all, which is what CASCADE writes.
ALTER TABLE game_templates
    DROP CONSTRAINT game_templates_upload_id_fkey;

ALTER TABLE game_templates
    ADD CONSTRAINT game_templates_upload_id_fkey
        FOREIGN KEY (upload_id) REFERENCES game_uploads ON DELETE CASCADE;
