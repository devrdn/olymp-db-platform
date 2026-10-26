-- Whether the play screen draws the schema as a diagram of its tables and
-- relations. A column of its own rather than a key in settings, for the
-- reason result_download is one: the default keeps the diagram on for every
-- existing contest and for any row written without it.
--
-- Adding a column with a constant default rewrites nothing, but it still
-- takes an exclusive lock on contests; a longer wait gives up rather than
-- queue every request that reads a contest behind it.
SET lock_timeout = '5s';

ALTER TABLE contests
    ADD COLUMN schema_diagram boolean NOT NULL DEFAULT true;
