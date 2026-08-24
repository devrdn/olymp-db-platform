-- Baseline roles and permissions. These codes are referenced by the
-- authorisation middleware, so they ship as a migration rather than as
-- optional seed data.

INSERT INTO roles (code, name) VALUES
    ('student',   'Student'),
    ('organizer', 'Contest organizer'),
    ('admin',     'System administrator');

INSERT INTO permissions (code, name) VALUES
    ('contest.create',    'Create contests'),
    ('contest.view',      'View a contest as staff'),
    ('contest.edit',      'Edit contest content and settings'),
    ('contest.publish',   'Publish and start a contest'),
    ('contest.manage',    'Appoint contest managers'),
    ('participant.manage','Add and remove contest participants'),
    ('reports.view',      'View contest reports and the query journal'),
    ('users.manage',      'Manage user accounts and global roles'),
    ('audit.view',        'View the audit trail');

-- Organizers run their own contests end to end, but never touch accounts.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN (
    'contest.create', 'contest.view', 'contest.edit', 'contest.publish',
    'contest.manage', 'participant.manage', 'reports.view'
)
WHERE r.code = 'organizer';

-- System administrators hold every permission.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.code = 'admin';

-- Students hold no staff permission: their access to a contest comes from a
-- registration, not from a role.
