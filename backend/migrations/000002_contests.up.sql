-- Contests, their content and their access rules.

CREATE TABLE contests (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL,
    description text,
    status      text NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'running', 'finished', 'archived')),
    enrollment  text NOT NULL DEFAULT 'invite_only'
        CHECK (enrollment IN ('open', 'invite_only')),
    -- Empty means no restriction; otherwise participation is limited to these
    -- networks (an on-site contest run from one lecture hall).
    allowed_cidrs cidr[] NOT NULL DEFAULT '{}',
    timing       text NOT NULL DEFAULT 'fixed'
        CHECK (timing IN ('fixed', 'individual')),
    duration_min int,
    starts_at    timestamptz,
    ends_at      timestamptz,
    settings     jsonb NOT NULL DEFAULT '{}',    -- query limits, grace period
    created_by   uuid NOT NULL REFERENCES users,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT contests_window_ordered
        CHECK (starts_at IS NULL OR ends_at IS NULL OR starts_at < ends_at),
    -- An individual contest is meaningless without a session length, and a
    -- fixed one must not carry a stray duration that nothing reads.
    CONSTRAINT contests_duration_matches_timing
        CHECK (
            (timing = 'individual' AND duration_min IS NOT NULL AND duration_min > 0)
            OR (timing = 'fixed' AND duration_min IS NULL)
        )
);

CREATE INDEX contests_status_starts_at_idx ON contests (status, starts_at);

-- Managers of a single contest. System administrators are not listed here:
-- their access comes from the global role.
CREATE TABLE contest_managers (
    contest_id uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'manager'
        CHECK (role IN ('owner', 'manager')),
    granted_by uuid NOT NULL REFERENCES users,
    granted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contest_id, user_id)
);

CREATE INDEX contest_managers_user_id_idx ON contest_managers (user_id);

-- Exactly one owner per contest: ownership carries the right to appoint
-- managers, so it must never be ambiguous.
CREATE UNIQUE INDEX contest_managers_single_owner_idx
    ON contest_managers (contest_id) WHERE role = 'owner';

-- How much SQL power participants get in this contest. Both the validator and
-- the database grants are derived from this single row.
CREATE TABLE contest_sql_policies (
    contest_id        uuid PRIMARY KEY REFERENCES contests ON DELETE CASCADE,
    mode              text NOT NULL DEFAULT 'read_only'
        CHECK (mode IN ('read_only', 'read_write')),
    writable_tables   text[] NOT NULL DEFAULT '{}',
    allow_create_view boolean NOT NULL DEFAULT false,
    allow_own_tables  boolean NOT NULL DEFAULT false,
    allow_temp_tables boolean NOT NULL DEFAULT false,
    -- Structural catalogs only; pg_database, pg_stat_activity and friends stay
    -- closed in every mode.
    allow_catalog     boolean NOT NULL DEFAULT true,
    disk_quota_ratio  int NOT NULL DEFAULT 5 CHECK (disk_quota_ratio > 0),
    updated_by        uuid REFERENCES users,
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT contest_sql_policies_writes_need_write_mode
        CHECK (mode = 'read_write' OR cardinality(writable_tables) = 0)
);

CREATE TABLE stories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id uuid NOT NULL UNIQUE REFERENCES contests ON DELETE CASCADE,
    body_md    text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE questions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id   uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    ord          int  NOT NULL,
    kind         text NOT NULL DEFAULT 'text'
        CHECK (kind IN ('text', 'choice', 'final')),
    body_md      text NOT NULL,
    points       int  NOT NULL DEFAULT 1 CHECK (points >= 0),
    max_attempts int  CHECK (max_attempts IS NULL OR max_attempts > 0),
    choices      jsonb,
    UNIQUE (contest_id, ord)
);

-- Reference answers. Never returned by participant-facing endpoints.
CREATE TABLE question_answers (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    question_id uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    match_kind  text NOT NULL DEFAULT 'exact_ci'
        CHECK (match_kind IN ('exact', 'exact_ci', 'regex')),
    value       text NOT NULL
);

CREATE INDEX question_answers_question_id_idx ON question_answers (question_id);
