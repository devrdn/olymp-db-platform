SET lock_timeout = '5s';

ALTER TABLE contests
    DROP CONSTRAINT IF EXISTS contests_self_registration_open,
    DROP COLUMN IF EXISTS archive_tasks,
    DROP COLUMN IF EXISTS visibility;
