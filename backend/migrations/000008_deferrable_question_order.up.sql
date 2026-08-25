-- Make the question ordering deferrable, so a reorder can be a single
-- statement instead of a dance around the constraint.
--
-- Swapping two questions means two rows briefly holding the same position.
-- With an immediate unique constraint that fails halfway through, and the only
-- ways around it are a temporary out-of-range position (which is visible to
-- anything reading concurrently) or one UPDATE per row in a fixed order (which
-- breaks as soon as the permutation contains a cycle). Deferring the check to
-- COMMIT lets the transaction pass through the inconsistent state that every
-- reordering necessarily has, while still refusing to commit a duplicate.
--
-- INITIALLY IMMEDIATE keeps the ordinary case unchanged: an INSERT that
-- collides still fails at the statement, where the error is easy to attribute.
-- Only a transaction that asks (SET CONSTRAINTS ... DEFERRED) gets the looser
-- timing.
--
-- One consequence worth naming: a deferrable constraint cannot back an
-- ON CONFLICT clause. Nothing upserts on (contest_id, ord) — positions are
-- assigned by the repository, never negotiated — but anything that later wants
-- to must use a different route.

ALTER TABLE questions DROP CONSTRAINT questions_contest_id_ord_key;

ALTER TABLE questions
    ADD CONSTRAINT questions_contest_id_ord_key
    UNIQUE (contest_id, ord) DEFERRABLE INITIALLY IMMEDIATE;
