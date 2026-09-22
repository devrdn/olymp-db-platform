-- Gives every contest its drawn cover back. The files on the volume are not
-- this statement's business and are left where they are: the sweep collects
-- what nothing refers to, and a rolled-back migration that also deleted
-- photographs would make the roll-back the unrecoverable step.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS contest_covers;
