-- What each participant did, counted as it happens instead of at every read.
--
-- The organiser's participants table showed one row per registration with a
-- dozen counters, and every one of them was a LATERAL aggregate over that
-- registration's whole range of query_log, submissions and participant_events.
-- The screen recomputes the table every three seconds, so at a contest's cap —
-- three hundred participants, a journal of a million and a half rows — the
-- whole history of the contest was read about twenty times a minute, and three
-- of the aggregates (the distinct addresses, the set of fingerprints and the
-- blind-answer test) read columns no serving index carries, so each of those
-- passes fetched every journal row from the heap as well.
--
-- Counters cannot be made cheap to recompute; they can be made unnecessary to
-- recompute. Each journal now maintains a summary row per registration as it
-- is written, and the table is one index range over the contest's
-- registrations joined to their summaries.
--
-- Maintained by triggers rather than by the code that writes the journals, for
-- the reason migration 000034 gives for contest_id: no writer has to remember,
-- and the summary and the journal can never disagree. The triggers are
-- statement-level and read their rows from transition tables, so a batch of
-- fifty signals, or a bulk insert of a hundred thousand journal rows, costs one
-- grouped UPDATE rather than one call per row.
--
-- One rule holds all of this together, and it is the reason the
-- identical-queries figure is *not* a counter here: **a journal write touches
-- only rows belonging to the registration whose row was written.** A counter
-- about what two participants have in common has to be added to both of them,
-- and every inserting transaction already holds its own row by then, so two
-- participants running each other's statements in the same moment take the
-- same two rows in opposite orders and one of them has their query refused
-- with a deadlock. Ordering cannot fix it — the first row each transaction
-- takes is its own. contest_query_fingerprints therefore records only who ran
-- what, each row written by the registration it belongs to, and the read
-- counts the shared ones (internal/postgres/watch.go, rosterSQL).

-- One row per registration, created by the first thing that registration does.
-- A registration that has done nothing has no row, and the read left-joins:
-- every counter then reads as the zero it is.
CREATE TABLE registration_activity (
    registration_id   uuid PRIMARY KEY REFERENCES registrations ON DELETE CASCADE,

    -- query_log: every journalled query, including one still running, as the
    -- aggregate this replaces counted them.
    queries           bigint NOT NULL DEFAULT 0,
    query_errors      bigint NOT NULL DEFAULT 0,
    query_rejected    bigint NOT NULL DEFAULT 0,
    -- How many distinct addresses those queries came from
    -- (registration_addresses holds which).
    addresses         bigint NOT NULL DEFAULT 0,
    -- How many of this registration's statements another participant also ran
    -- is NOT here: it is the one figure on the table that is about more than
    -- one registration, so keeping it would mean one participant's query
    -- writing another participant's row. contest_query_fingerprints holds the
    -- set instead and the read counts over it.

    -- submissions.
    correct           bigint NOT NULL DEFAULT 0,
    wrong             bigint NOT NULL DEFAULT 0,
    blind             bigint NOT NULL DEFAULT 0,

    -- participant_events. events counts the whole table and is what the write
    -- side checks its per-registration quota against
    -- (monitor.MaxStoredEvents).
    events            bigint NOT NULL DEFAULT 0,
    page_left         bigint NOT NULL DEFAULT 0,
    away_ms           bigint NOT NULL DEFAULT 0,
    pastes            bigint NOT NULL DEFAULT 0,
    -- The longest paste into the SQL editor or an answer. A count of the pastes
    -- past a threshold would put that threshold here as well as in Go; the
    -- largest paste is the same decision with the threshold left where it
    -- belongs (monitor.LargePasteChars, applied by RosterRow.Flags).
    max_paste_chars   bigint NOT NULL DEFAULT 0,
    ip_changes        bigint NOT NULL DEFAULT 0,
    parallel_sessions bigint NOT NULL DEFAULT 0,

    -- The latest of each source; the table shows the latest of the three.
    last_query_at     timestamptz,
    last_answer_at    timestamptz,
    last_event_at     timestamptz
);

COMMENT ON TABLE registration_activity IS
    'Derived counters for the organiser''s participants table, maintained by the journals'' triggers.';

-- Which addresses a registration''s queries came from, so that a repeat of one
-- already seen costs nothing. Read only by the trigger that fills it: the
-- participants table reads the count beside the other counters. One row per
-- distinct address, so unlike the journal it does not grow with repetition.
CREATE TABLE registration_addresses (
    registration_id uuid NOT NULL REFERENCES registrations ON DELETE CASCADE,
    ip              inet NOT NULL,
    PRIMARY KEY (registration_id, ip)
);

-- Which registration of which contest has run which comparable fingerprint
-- successfully. Only a statement of at least monitor.IdenticalQueryMinChars
-- normalised characters has a fingerprint at all (ComparableFingerprint,
-- applied as the row is written), so the threshold is not applied here either.
--
-- The contest is named directly and through the registration, and the composite
-- foreign key holds the two to agree, as participant_events does since 000033.
CREATE TABLE contest_query_fingerprints (
    contest_id      uuid   NOT NULL,
    fingerprint     bigint NOT NULL,
    registration_id uuid   NOT NULL,
    PRIMARY KEY (contest_id, fingerprint, registration_id),
    CONSTRAINT contest_query_fingerprints_registration_contest_fkey
        FOREIGN KEY (registration_id, contest_id)
        REFERENCES registrations (id, contest_id) ON DELETE CASCADE
);

-- The cascade from registrations, which the primary key cannot serve because
-- it leads with the contest (CLAUDE.md rule 7).
CREATE INDEX contest_query_fingerprints_registration_idx
    ON contest_query_fingerprints (registration_id, contest_id);

-- Whether a correct answer had no successful query behind it, decided once,
-- when the answer is given, instead of by a NOT EXISTS per answer per read.
-- A constant default makes this metadata only: no rewrite of the table.
ALTER TABLE submissions ADD COLUMN blind boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN submissions.blind IS
    'Correct, and no successful query of the same participant since their previous answer (design §5).';

-- The queries named by ids have become successful — inserted that way, or
-- completed into it. Two things follow, and they are written once here because
-- both the insert and the update trigger need them.
CREATE FUNCTION monitoring_queries_succeeded(ids bigint[]) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    -- This registration has now run these fingerprints successfully. The row
    -- names the registration that ran the query and nothing else: which
    -- statements more than one participant ran is worked out when the
    -- organiser's table is read.
    --
    -- Deliberately not a counter kept here. A counter would have to be added
    -- to the participant who until then held the fingerprint alone — a write
    -- to another registration's row — and every inserting transaction already
    -- holds its own row by the time it gets here. Two participants running
    -- each other's statements in the same moment would take the same two rows
    -- in opposite orders, and one of them would have their query refused with
    -- a deadlock. Ordering the locks cannot help: the first row each
    -- transaction takes is its own, and which one that is depends on who is
    -- typing. A room of students pasting the same starter query is the
    -- ordinary case, not a rare one.
    INSERT INTO contest_query_fingerprints (contest_id, fingerprint, registration_id)
    SELECT DISTINCT q.contest_id, q.sql_fingerprint, q.registration_id
    FROM query_log q
    WHERE q.id = ANY(ids)
      AND q.status = 'ok'
      AND q.sql_fingerprint IS NOT NULL
      AND q.contest_id IS NOT NULL
    ON CONFLICT DO NOTHING;

    -- An answer that was counted blind only because the query behind it had
    -- not finished when it was given. A journalled query is written before it
    -- runs and completed afterwards, so a participant who answers while their
    -- query is still in flight would otherwise be marked as having answered
    -- blind for as long as the row says 'running'. The answer to correct is
    -- the earliest one after the query: by being the earliest, its window
    -- starts at or before this query, which is exactly the window the read-time
    -- aggregate used.
    WITH affected AS (
        SELECT DISTINCT s.id
        FROM query_log q
        CROSS JOIN LATERAL (
            SELECT later.id, later.blind
            FROM submissions later
            WHERE later.registration_id = q.registration_id
              AND later.submitted_at > q.executed_at
            ORDER BY later.submitted_at, later.id
            LIMIT 1
        ) s
        WHERE q.id = ANY(ids) AND q.status = 'ok' AND s.blind
    ), cleared AS (
        UPDATE submissions s SET blind = false
        WHERE s.id IN (SELECT id FROM affected)
        RETURNING s.registration_id
    )
    UPDATE registration_activity a
    SET blind = a.blind - t.n
    FROM (SELECT registration_id, count(*) AS n FROM cleared GROUP BY registration_id) t
    WHERE a.registration_id = t.registration_id;
END;
$$;

COMMENT ON FUNCTION monitoring_queries_succeeded(bigint[]) IS
    'Folds newly successful queries into the participants table''s counters.';

CREATE FUNCTION monitoring_queries_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO registration_activity AS a
        (registration_id, queries, query_errors, query_rejected, last_query_at)
    SELECT n.registration_id, count(*),
           count(*) FILTER (WHERE n.status IN ('error', 'timeout')),
           count(*) FILTER (WHERE n.status = 'rejected'),
           max(n.executed_at)
    FROM inserted n
    GROUP BY n.registration_id
    ON CONFLICT (registration_id) DO UPDATE SET
        queries        = a.queries + excluded.queries,
        query_errors   = a.query_errors + excluded.query_errors,
        query_rejected = a.query_rejected + excluded.query_rejected,
        last_query_at  = greatest(a.last_query_at, excluded.last_query_at);

    -- An address counts once however many queries came from it.
    WITH fresh AS (
        INSERT INTO registration_addresses (registration_id, ip)
        SELECT DISTINCT n.registration_id, n.ip FROM inserted n WHERE n.ip IS NOT NULL
        ON CONFLICT DO NOTHING
        RETURNING registration_id
    )
    UPDATE registration_activity a
    SET addresses = a.addresses + t.n
    FROM (SELECT registration_id, count(*) AS n FROM fresh GROUP BY registration_id) t
    WHERE a.registration_id = t.registration_id;

    -- The console writes its row at 'running' and completes it afterwards, so
    -- on that path there is nothing successful here and nothing to do.
    IF EXISTS (SELECT 1 FROM inserted n WHERE n.status = 'ok') THEN
        PERFORM monitoring_queries_succeeded(
            array(SELECT n.id FROM inserted n WHERE n.status = 'ok'));
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION monitoring_queries_completed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- A query moves between the buckets the table counts: out of the one its
    -- old status belonged to, into the one its new status belongs to. Written
    -- as a difference rather than as two passes because a status may change
    -- more than once — a row the sweeper gave up on may still be closed by a
    -- late but truthful result.
    WITH moved AS (
        SELECT n.registration_id,
               (CASE WHEN n.status IN ('error', 'timeout') THEN 1 ELSE 0 END)
             - (CASE WHEN o.status IN ('error', 'timeout') THEN 1 ELSE 0 END) AS errors,
               (CASE WHEN n.status = 'rejected' THEN 1 ELSE 0 END)
             - (CASE WHEN o.status = 'rejected' THEN 1 ELSE 0 END) AS rejected
        FROM completed n
        JOIN opened o ON o.id = n.id
        WHERE n.status IS DISTINCT FROM o.status
    )
    UPDATE registration_activity a
    SET query_errors   = a.query_errors + t.errors,
        query_rejected = a.query_rejected + t.rejected
    FROM (SELECT registration_id, sum(errors) AS errors, sum(rejected) AS rejected
          FROM moved GROUP BY registration_id) t
    WHERE a.registration_id = t.registration_id;

    IF EXISTS (SELECT 1 FROM completed n JOIN opened o ON o.id = n.id
               WHERE n.status = 'ok' AND o.status IS DISTINCT FROM 'ok') THEN
        PERFORM monitoring_queries_succeeded(array(
            SELECT n.id FROM completed n JOIN opened o ON o.id = n.id
            WHERE n.status = 'ok' AND o.status IS DISTINCT FROM 'ok'));
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION monitoring_answers_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- A correct answer is blind when no successful query of the same
    -- participant ran between their previous answer to any question (or the
    -- beginning) and this one — design §3's window, and §5's flag. Both bounds
    -- are index conditions: the previous answer is one step back along
    -- submissions_registration_submitted_idx, the queries one range of
    -- query_log_registration_executed_idx.
    --
    -- The marking CTE is not selected from; a data-modifying CTE runs whether
    -- it is read or not, and the counters below need the same decision, not the
    -- row it wrote.
    WITH judged AS (
        SELECT n.id, n.registration_id, n.is_correct, n.submitted_at,
               n.is_correct AND NOT EXISTS (
                   SELECT 1 FROM query_log q
                   WHERE q.registration_id = n.registration_id
                     AND q.status = 'ok'
                     AND q.executed_at >= COALESCE((
                             SELECT p.submitted_at FROM submissions p
                             WHERE p.registration_id = n.registration_id
                               AND (p.submitted_at, p.id) < (n.submitted_at, n.id)
                             ORDER BY p.submitted_at DESC, p.id DESC
                             LIMIT 1), '-infinity')
                     AND q.executed_at < n.submitted_at) AS blind
        FROM inserted n
    ), marked AS (
        UPDATE submissions s SET blind = true
        FROM judged j WHERE s.id = j.id AND j.blind
        RETURNING s.id
    )
    INSERT INTO registration_activity AS a
        (registration_id, correct, wrong, blind, last_answer_at)
    SELECT j.registration_id,
           count(*) FILTER (WHERE j.is_correct),
           count(*) FILTER (WHERE NOT j.is_correct),
           count(*) FILTER (WHERE j.blind),
           max(j.submitted_at)
    FROM judged j
    GROUP BY j.registration_id
    ON CONFLICT (registration_id) DO UPDATE SET
        correct        = a.correct + excluded.correct,
        wrong          = a.wrong + excluded.wrong,
        blind          = a.blind + excluded.blind,
        last_answer_at = greatest(a.last_answer_at, excluded.last_answer_at);
    RETURN NULL;
END;
$$;

CREATE FUNCTION monitoring_events_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO registration_activity AS a
        (registration_id, events, page_left, away_ms, pastes, max_paste_chars,
         ip_changes, parallel_sessions, last_event_at)
    SELECT n.registration_id,
           count(*),
           count(*) FILTER (WHERE n.kind = 'page_left'),
           COALESCE(sum((n.payload->>'away_ms')::bigint) FILTER (WHERE n.kind = 'page_left'), 0),
           -- A paste folded from identical ones in a row (monitor.CleanBatch)
           -- counts as all of them.
           COALESCE(sum(COALESCE((n.payload->>'count')::bigint, 1)) FILTER (WHERE n.kind = 'paste'), 0),
           COALESCE(max((n.payload->>'chars')::bigint) FILTER (
               WHERE n.kind = 'paste' AND n.payload->>'target' IN ('editor', 'answer')), 0),
           count(*) FILTER (WHERE n.kind = 'ip_changed'),
           count(*) FILTER (WHERE n.kind = 'parallel_session'),
           max(n.created_at)
    FROM inserted n
    GROUP BY n.registration_id
    ON CONFLICT (registration_id) DO UPDATE SET
        events            = a.events + excluded.events,
        page_left         = a.page_left + excluded.page_left,
        away_ms           = a.away_ms + excluded.away_ms,
        pastes            = a.pastes + excluded.pastes,
        max_paste_chars   = greatest(a.max_paste_chars, excluded.max_paste_chars),
        ip_changes        = a.ip_changes + excluded.ip_changes,
        parallel_sessions = a.parallel_sessions + excluded.parallel_sessions,
        last_event_at     = greatest(a.last_event_at, excluded.last_event_at);
    RETURN NULL;
END;
$$;

CREATE TRIGGER query_log_activity_insert
    AFTER INSERT ON query_log
    REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION monitoring_queries_inserted();

-- On every update, not only on one naming status: a trigger that asks for
-- transition tables may not also carry a column list. The function compares
-- the two tables and does nothing when no status moved, which is the same
-- filter one statement later.
CREATE TRIGGER query_log_activity_complete
    AFTER UPDATE ON query_log
    REFERENCING OLD TABLE AS opened NEW TABLE AS completed
    FOR EACH STATEMENT EXECUTE FUNCTION monitoring_queries_completed();

CREATE TRIGGER submissions_activity_insert
    AFTER INSERT ON submissions
    REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION monitoring_answers_inserted();

CREATE TRIGGER participant_events_activity_insert
    AFTER INSERT ON participant_events
    REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION monitoring_events_inserted();

-- What the journals already hold. One pass over each of them, and the same
-- aggregates the read used to do on every refresh — run once here instead of
-- twenty times a minute. Taking row locks only on submissions and none at all
-- on the journals it reads; the tables it fills are new and nothing is reading
-- them yet. Run it in a deployment window rather than during a contest.

INSERT INTO registration_addresses (registration_id, ip)
SELECT DISTINCT q.registration_id, q.ip FROM query_log q WHERE q.ip IS NOT NULL;

INSERT INTO contest_query_fingerprints (contest_id, fingerprint, registration_id)
SELECT DISTINCT q.contest_id, q.sql_fingerprint, q.registration_id
FROM query_log q
WHERE q.status = 'ok' AND q.sql_fingerprint IS NOT NULL AND q.contest_id IS NOT NULL;

UPDATE submissions s SET blind = true
FROM (
    SELECT a.id
    FROM (SELECT s2.id, s2.registration_id, s2.is_correct, s2.submitted_at,
                 lag(s2.submitted_at) OVER (PARTITION BY s2.registration_id
                                            ORDER BY s2.submitted_at, s2.id) AS previous
          FROM submissions s2) a
    WHERE a.is_correct AND NOT EXISTS (
        SELECT 1 FROM query_log q
        WHERE q.registration_id = a.registration_id
          AND q.status = 'ok'
          AND q.executed_at < a.submitted_at
          AND q.executed_at >= COALESCE(a.previous, '-infinity'))
) b
WHERE s.id = b.id;

INSERT INTO registration_activity (registration_id)
SELECT r.id FROM registrations r
WHERE EXISTS (SELECT 1 FROM query_log q WHERE q.registration_id = r.id)
   OR EXISTS (SELECT 1 FROM submissions s WHERE s.registration_id = r.id)
   OR EXISTS (SELECT 1 FROM participant_events e WHERE e.registration_id = r.id);

UPDATE registration_activity a
SET queries = t.total, query_errors = t.errors, query_rejected = t.rejected, last_query_at = t.last_at
FROM (SELECT registration_id, count(*) AS total,
             count(*) FILTER (WHERE status IN ('error', 'timeout')) AS errors,
             count(*) FILTER (WHERE status = 'rejected') AS rejected,
             max(executed_at) AS last_at
      FROM query_log GROUP BY registration_id) t
WHERE a.registration_id = t.registration_id;

UPDATE registration_activity a
SET addresses = t.n
FROM (SELECT registration_id, count(*) AS n FROM registration_addresses GROUP BY registration_id) t
WHERE a.registration_id = t.registration_id;


UPDATE registration_activity a
SET correct = t.correct, wrong = t.wrong, blind = t.blind, last_answer_at = t.last_at
FROM (SELECT registration_id,
             count(*) FILTER (WHERE is_correct) AS correct,
             count(*) FILTER (WHERE NOT is_correct) AS wrong,
             count(*) FILTER (WHERE blind) AS blind,
             max(submitted_at) AS last_at
      FROM submissions GROUP BY registration_id) t
WHERE a.registration_id = t.registration_id;

UPDATE registration_activity a
SET events = t.events, page_left = t.page_left, away_ms = t.away_ms, pastes = t.pastes,
    max_paste_chars = t.max_chars, ip_changes = t.ip_changes,
    parallel_sessions = t.parallel, last_event_at = t.last_at
FROM (SELECT registration_id,
             count(*) AS events,
             count(*) FILTER (WHERE kind = 'page_left') AS page_left,
             COALESCE(sum((payload->>'away_ms')::bigint) FILTER (WHERE kind = 'page_left'), 0) AS away_ms,
             COALESCE(sum(COALESCE((payload->>'count')::bigint, 1)) FILTER (WHERE kind = 'paste'), 0) AS pastes,
             COALESCE(max((payload->>'chars')::bigint) FILTER (
                 WHERE kind = 'paste' AND payload->>'target' IN ('editor', 'answer')), 0) AS max_chars,
             count(*) FILTER (WHERE kind = 'ip_changed') AS ip_changes,
             count(*) FILTER (WHERE kind = 'parallel_session') AS parallel,
             max(created_at) AS last_at
      FROM participant_events GROUP BY registration_id) t
WHERE a.registration_id = t.registration_id;
