-- Discards the quota monitor's flags; the next sweep would set them again.
SET lock_timeout = '5s';

ALTER TABLE game_instances DROP COLUMN IF EXISTS over_quota_since;
