-- Safe to run against a populated table, but not free: a contest scored in
-- icpc mode cannot keep that mode under the narrower CHECK below, so it is
-- moved to points first, and its penalty setting is dropped with the column.
-- Both are discarded, not archived. Reapplying the up migration afterwards
-- restores the column at its default, and every such contest stays on points
-- until an organizer changes it back.
UPDATE contests SET scoring = 'points' WHERE scoring = 'icpc';

ALTER TABLE contests DROP COLUMN IF EXISTS icpc_penalty_min;
ALTER TABLE contests DROP CONSTRAINT contests_scoring_check;
ALTER TABLE contests
    ADD CONSTRAINT contests_scoring_check CHECK (scoring IN ('points', 'winner'));
