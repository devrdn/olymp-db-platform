-- Participant answers and the two journals: the SQL query log and the audit
-- trail.

CREATE TABLE submissions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registration_id uuid NOT NULL REFERENCES registrations ON DELETE CASCADE,
    question_id     uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    attempt_no      int  NOT NULL CHECK (attempt_no > 0),
    value           text NOT NULL,
    is_correct      boolean NOT NULL,
    points_awarded  int  NOT NULL DEFAULT 0 CHECK (points_awarded >= 0),
    submitted_at    timestamptz NOT NULL DEFAULT now(),

    UNIQUE (registration_id, question_id, attempt_no)
);

CREATE INDEX submissions_question_id_idx ON submissions (question_id);

-- Every SQL statement a participant runs, including the ones the validator
-- rejected. Written in two phases: the row is created before execution and
-- completed afterwards, so a crash mid-query cannot lose the record.
CREATE TABLE query_log (
    id              bigserial PRIMARY KEY,
    registration_id uuid NOT NULL REFERENCES registrations ON DELETE CASCADE,
    request_id      uuid NOT NULL,               -- links to the technical logs
    sql_text        text NOT NULL,
    status          text NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'ok', 'rejected', 'error', 'timeout')),
    error_text      text,
    duration_ms     int,
    row_count       int,
    executed_at     timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz
);

-- Serves the participant's own history and the admin journal panel.
CREATE INDEX query_log_registration_executed_idx ON query_log (registration_id, executed_at DESC);
-- Serves keyset pagination of the journal panel.
CREATE INDEX query_log_executed_id_idx ON query_log (executed_at DESC, id DESC);
-- Finds rows abandoned by a crashed process.
CREATE INDEX query_log_running_idx ON query_log (executed_at) WHERE status = 'running';
-- Full-text search over statements ("who queried the suspects table?").
CREATE INDEX query_log_sql_text_idx ON query_log USING gin (to_tsvector('simple', sql_text));

-- Append-only trail of everything users and administrators do.
CREATE TABLE audit_log (
    id         bigserial PRIMARY KEY,
    actor_id   uuid REFERENCES users,            -- NULL for system events
    action     text NOT NULL,                    -- auth.login, contest.create, ...
    entity     text,
    entity_id  text,
    payload    jsonb,
    ip         inet,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_created_at_idx ON audit_log (created_at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, created_at DESC);
CREATE INDEX audit_log_action_idx ON audit_log (action, created_at DESC);
