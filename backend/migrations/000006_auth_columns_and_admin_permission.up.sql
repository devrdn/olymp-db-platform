-- Columns the authentication flow needs, and the permission that lifts the
-- contest scope for installation-wide administrators.

ALTER TABLE users
    -- Advancing this retires every session issued before it. Blocking an
    -- account or changing its password bumps it, which is how "log out
    -- everywhere" works without keeping an index of live sessions.
    ADD COLUMN session_generation int NOT NULL DEFAULT 0,
    -- Accounts are created by an administrator with a one-time password, so
    -- the first login has to end in the user choosing their own.
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false,
    ADD COLUMN password_changed_at timestamptz,
    ADD COLUMN last_login_at timestamptz;

INSERT INTO permissions (code, name) VALUES
    ('contest.admin_all', 'Act on every contest without being its manager');

-- Only system administrators hold it. It is a permission rather than a
-- hard-coded role check so another role (a dean's office account, an auditor)
-- can be given the same reach from data alone.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code = 'contest.admin_all'
WHERE r.code = 'admin';

-- Organizers keep only the installation-wide ability to create a contest.
-- Their power over a particular contest comes from being listed as its owner
-- in contest_managers, not from a global grant: holding contest.edit globally
-- would otherwise read as "may edit anybody's contest", which is exactly what
-- the second authorisation level exists to prevent.
DELETE FROM role_permissions
WHERE role_id = (SELECT id FROM roles WHERE code = 'organizer')
  AND permission_id IN (
      SELECT id FROM permissions
      WHERE code IN ('contest.edit', 'contest.publish', 'contest.manage',
                     'participant.manage', 'reports.view')
  );
