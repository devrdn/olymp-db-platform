-- How many places down the standings earn a first, second and third prize.
-- Columns of their own rather than keys in settings, because an update
-- replaces settings whole. The default awards nothing, so every existing
-- contest shows no prizes until its organiser sets them; the bound repeats
-- the domain's (contests.maxPrizePlaces).
--
-- Adding a column with a constant default rewrites nothing, but it still
-- takes an exclusive lock on contests; a longer wait gives up rather than
-- queue every request that reads a contest behind it.
SET lock_timeout = '5s';

ALTER TABLE contests
    ADD COLUMN prize_first integer NOT NULL DEFAULT 0
        CONSTRAINT contests_prize_first_range CHECK (prize_first BETWEEN 0 AND 1000),
    ADD COLUMN prize_second integer NOT NULL DEFAULT 0
        CONSTRAINT contests_prize_second_range CHECK (prize_second BETWEEN 0 AND 1000),
    ADD COLUMN prize_third integer NOT NULL DEFAULT 0
        CONSTRAINT contests_prize_third_range CHECK (prize_third BETWEEN 0 AND 1000);
