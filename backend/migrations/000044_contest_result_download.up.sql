-- Whether a participant is offered a query result as a CSV file. A column of
-- its own rather than a key in settings: a missing jsonb key would read
-- false, turning the download off on every existing contest, while the
-- default here keeps it on for them and for any row written without it.
--
-- Adding a column with a constant default rewrites nothing, but it still
-- takes an exclusive lock on contests; five seconds is longer than any query
-- the API may run, so a longer wait gives up rather than queue every request
-- that reads a contest behind it.
SET lock_timeout = '5s';

ALTER TABLE contests
    ADD COLUMN result_download boolean NOT NULL DEFAULT true;
