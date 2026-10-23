ALTER TABLE contests
    DROP COLUMN IF EXISTS prize_first,
    DROP COLUMN IF EXISTS prize_second,
    DROP COLUMN IF EXISTS prize_third;
