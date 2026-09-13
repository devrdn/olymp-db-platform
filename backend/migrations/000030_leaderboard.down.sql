DROP INDEX IF EXISTS submissions_registration_submitted_idx;
ALTER TABLE contests DROP COLUMN IF EXISTS leaderboard_revealed_at;
ALTER TABLE contests DROP COLUMN IF EXISTS leaderboard_names;
ALTER TABLE contests DROP COLUMN IF EXISTS leaderboard_freeze_min;
