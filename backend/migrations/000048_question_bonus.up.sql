-- A bonus question (docs/ARCHITECTURE.md §6.1.1). False for every existing
-- question.
--
-- A constant default rewrites nothing, but the column still takes an
-- exclusive lock on questions; a longer wait gives up rather than queue
-- participants' question reads behind it.
SET lock_timeout = '5s';

ALTER TABLE questions ADD COLUMN is_bonus boolean NOT NULL DEFAULT false;
