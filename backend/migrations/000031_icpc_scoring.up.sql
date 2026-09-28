-- The ICPC scoring mode (docs/ARCHITECTURE.md §6.1.1):
-- place is decided by how many questions a registration solved and how much
-- penalty time solving them cost, never by points.
--
-- The existing CHECK on contests.scoring (000018) only allows 'points' and
-- 'winner'. It is dropped and re-added under its own explicit name — rather
-- than left to whatever name PostgreSQL happened to generate for the
-- original inline CHECK — so the down migration can name it back precisely.
ALTER TABLE contests DROP CONSTRAINT contests_scoring_check;
ALTER TABLE contests
    ADD CONSTRAINT contests_scoring_check CHECK (scoring IN ('points', 'winner', 'icpc'));

-- How many minutes one wrong attempt costs a solved question in ICPC scoring.
-- Meaningless in every other mode, but always present and always bounded
-- (CLAUDE.md rule 2), so a contest that switches back to icpc later has a
-- value ready rather than a fresh default nobody chose. NOT NULL DEFAULT 20
-- keeps every row already in the table valid without a backfill statement.
ALTER TABLE contests
    ADD COLUMN icpc_penalty_min smallint NOT NULL DEFAULT 20
        CHECK (icpc_penalty_min BETWEEN 0 AND 240);
