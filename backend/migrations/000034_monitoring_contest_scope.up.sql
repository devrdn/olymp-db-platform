-- The contest a journalled query or answer belongs to, written onto the
-- journal row itself.
--
-- The organiser's live feed of a whole contest merges every source by time
-- and keeps one page. participant_events has carried its contest since
-- migration 000033 and is read as one range of one index; query_log and
-- submissions carried only the registration, so the same page had to be
-- asked of every registration separately — a page of rows fetched and sorted
-- for each of three hundred participants to return one page, twenty times a
-- minute while the screen is open. With the contest on the row, each of them
-- is one range of one index too, and the read costs what it returns.
--
-- Derived, not supplied: the trigger below fills the column from the
-- registration on every insert, so the two can never disagree and no writer
-- has to remember it. registrations.contest_id never changes and a
-- registration's journal rows are deleted with it, so the value needs no
-- maintenance afterwards. The column is left nullable rather than made NOT
-- NULL: the constraint costs a scan of the whole journal under a lock that
-- blocks writers, and buys nothing the trigger does not already guarantee.

ALTER TABLE query_log ADD COLUMN contest_id uuid;
ALTER TABLE submissions ADD COLUMN contest_id uuid;

-- Both tables name the registration and the contest by the same column
-- names, so one function serves both triggers.
CREATE FUNCTION monitoring_fill_contest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT r.contest_id INTO NEW.contest_id
    FROM registrations r
    WHERE r.id = NEW.registration_id;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION monitoring_fill_contest() IS
    'Fills a journal row''s contest_id from its registration: the column is derived, never supplied.';

CREATE TRIGGER query_log_contest_fill
    BEFORE INSERT ON query_log
    FOR EACH ROW EXECUTE FUNCTION monitoring_fill_contest();

CREATE TRIGGER submissions_contest_fill
    BEFORE INSERT ON submissions
    FOR EACH ROW EXECUTE FUNCTION monitoring_fill_contest();

-- The rows written before the triggers existed. One pass over each journal,
-- taking row locks only: no reader waits for it, and an insert waits only on
-- the trigger creation above, which this migration commits together with the
-- backfill. Run it in a deployment window rather than during a contest.
UPDATE query_log q
SET contest_id = r.contest_id
FROM registrations r
WHERE r.id = q.registration_id AND q.contest_id IS NULL;

UPDATE submissions s
SET contest_id = r.contest_id
FROM registrations r
WHERE r.id = s.registration_id AND s.contest_id IS NULL;
