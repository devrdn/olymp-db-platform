-- Gives the participants table back to the read: the counters, the sets they
-- were kept from, and the flag on an answer all go, and the aggregates in
-- internal/postgres/watch.go are what tells the organiser what a participant
-- did again.
DROP TRIGGER IF EXISTS contest_query_fingerprints_activity_delete ON contest_query_fingerprints;
DROP TRIGGER IF EXISTS participant_events_activity_insert ON participant_events;
DROP TRIGGER IF EXISTS submissions_activity_insert ON submissions;
DROP TRIGGER IF EXISTS query_log_activity_complete ON query_log;
DROP TRIGGER IF EXISTS query_log_activity_insert ON query_log;

DROP FUNCTION IF EXISTS monitoring_fingerprints_deleted();
DROP FUNCTION IF EXISTS monitoring_events_inserted();
DROP FUNCTION IF EXISTS monitoring_answers_inserted();
DROP FUNCTION IF EXISTS monitoring_queries_completed();
DROP FUNCTION IF EXISTS monitoring_queries_inserted();
DROP FUNCTION IF EXISTS monitoring_queries_succeeded(bigint[]);

ALTER TABLE submissions DROP COLUMN IF EXISTS blind;

DROP TABLE IF EXISTS contest_query_fingerprints;
DROP TABLE IF EXISTS registration_addresses;
DROP TABLE IF EXISTS registration_activity;
