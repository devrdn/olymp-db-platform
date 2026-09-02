ALTER TABLE contests DROP CONSTRAINT IF EXISTS contests_policy_coherent;

ALTER TABLE contests
    DROP COLUMN IF EXISTS allow_catalog,
    DROP COLUMN IF EXISTS allow_temp_tables,
    DROP COLUMN IF EXISTS allow_own_tables,
    DROP COLUMN IF EXISTS allow_create_view,
    DROP COLUMN IF EXISTS writable_tables,
    DROP COLUMN IF EXISTS sql_mode;

DROP DOMAIN IF EXISTS plain_identifier;
