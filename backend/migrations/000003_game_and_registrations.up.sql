-- Game templates, participant registrations and the per-participant database
-- instances provisioned from a template.

CREATE TABLE game_templates (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id  uuid NOT NULL UNIQUE REFERENCES contests ON DELETE CASCADE,
    template_db text NOT NULL,                   -- game_tpl_c{short_id}
    -- Bumped on every rebuild. Instances created from an older version are
    -- dropped and recreated, so nobody plays with stale data or grants.
    version     int  NOT NULL DEFAULT 1 CHECK (version > 0),
    init_script text NOT NULL,                   -- source SQL (DDL + data)
    status      text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'building', 'ready', 'failed')),
    build_error text,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE registrations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    contest_id  uuid NOT NULL REFERENCES contests ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'registered'
        CHECK (status IN ('registered', 'active', 'finished', 'disqualified')),
    -- With individual timing this is what the participant deadline is computed
    -- from; with fixed timing it is recorded for analytics only.
    started_at  timestamptz,
    finished_at timestamptz,
    -- Denormalised for the leaderboard; recalculated in the same transaction
    -- as every accepted answer.
    total_score int NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (contest_id, user_id),
    CONSTRAINT registrations_finish_after_start
        CHECK (finished_at IS NULL OR started_at IS NOT NULL AND finished_at >= started_at)
);

CREATE INDEX registrations_user_id_idx ON registrations (user_id);
-- Serves the leaderboard read path.
CREATE INDEX registrations_contest_score_idx ON registrations (contest_id, total_score DESC);

CREATE TABLE game_instances (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registration_id  uuid NOT NULL UNIQUE REFERENCES registrations ON DELETE CASCADE,
    db_name          text NOT NULL UNIQUE,
    template_version int  NOT NULL DEFAULT 1,
    status           text NOT NULL DEFAULT 'provisioning'
        CHECK (status IN ('provisioning', 'ready', 'failed', 'dropped')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX game_instances_status_idx ON game_instances (status);
