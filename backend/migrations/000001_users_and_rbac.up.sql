-- Users and the two-level access model: global roles plus per-contest managers.
-- Permissions are data, not code, so a new role is a row rather than a release.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login         text NOT NULL UNIQUE,          -- student card number or username
    email         text UNIQUE,
    password_hash text NOT NULL,                 -- argon2id
    full_name     text NOT NULL,
    status        text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'blocked')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Logins are compared case-insensitively, so uniqueness must be too:
-- "Ivanov" and "ivanov" may not become two accounts.
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login));

CREATE TABLE roles (
    id   smallserial PRIMARY KEY,
    code text NOT NULL UNIQUE,                   -- student, organizer, admin
    name text NOT NULL
);

CREATE TABLE permissions (
    id   smallserial PRIMARY KEY,
    code text NOT NULL UNIQUE,                   -- contest.create, users.manage, ...
    name text NOT NULL
);

CREATE TABLE role_permissions (
    role_id       smallint NOT NULL REFERENCES roles ON DELETE CASCADE,
    permission_id smallint NOT NULL REFERENCES permissions ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE user_roles (
    user_id uuid     NOT NULL REFERENCES users ON DELETE CASCADE,
    role_id smallint NOT NULL REFERENCES roles ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX user_roles_role_id_idx ON user_roles (role_id);
