-- When the quota monitor first saw an instance at or past its disk quota, or
-- NULL while it is within. It is a report only: the Query Runner refuses a
-- growing write on its own check, whatever this column says. No index: it is
-- read with the rows of one contest, and written by database name.

SET lock_timeout = '5s';

ALTER TABLE game_instances ADD COLUMN over_quota_since timestamptz;
