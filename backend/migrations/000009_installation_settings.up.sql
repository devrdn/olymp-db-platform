-- Settings an administrator changes without a deploy.
--
-- A row rather than an environment variable, and the difference is practical:
-- a variable needs a container restart and a shell on the server, which makes
-- changing the university's name work for whoever has SSH rather than for
-- whoever runs the olympiad.
--
-- The boundary is drawn by consequence, not by type. What the process cannot
-- start without, or what is secret, stays in the environment: the database
-- DSN, the cache address, TRUSTED_PROXIES, COOKIE_SECURE. What is an
-- organisation's decision lives here.
CREATE TABLE settings (
    key        text PRIMARY KEY,
    -- jsonb rather than text: a setting is not always a string, and a value
    -- that has to be parsed by every reader is a value every reader can parse
    -- differently.
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Who last changed it. The audit trail holds the history; this answers
    -- "who is responsible for what is on screen right now" without a search.
    updated_by uuid REFERENCES users ON DELETE SET NULL
);

INSERT INTO permissions (code, name) VALUES
    ('settings.manage', 'Change installation settings');

-- Only system administrators. Naming the university and replacing its logo is
-- not something contest staff do, and an organizer who could would be
-- rebranding an installation from inside a contest they happen to run.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code = 'settings.manage'
WHERE r.code = 'admin';
