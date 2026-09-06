-- Safe to run against a populated table — these are plain column drops, no
-- backfill to undo — but not free: any penalty percentage, progression or
-- scoring mode an organizer already configured is discarded, not archived,
-- the moment this runs. Reapplying the up migration afterwards restores the
-- columns at their defaults, not the values that were here before.
ALTER TABLE contests DROP COLUMN scoring;
ALTER TABLE contests DROP COLUMN progression;
ALTER TABLE questions DROP COLUMN penalty_pct;
