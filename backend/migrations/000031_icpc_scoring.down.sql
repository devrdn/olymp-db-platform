ALTER TABLE contests DROP COLUMN IF EXISTS icpc_penalty_min;
ALTER TABLE contests DROP CONSTRAINT contests_scoring_check;
ALTER TABLE contests
    ADD CONSTRAINT contests_scoring_check CHECK (scoring IN ('points', 'winner'));
