-- Restore the global grants organizers held before this migration.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code IN ('contest.edit', 'contest.publish', 'contest.manage',
                                 'participant.manage', 'reports.view')
WHERE r.code = 'organizer'
ON CONFLICT DO NOTHING;

DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'contest.admin_all');

DELETE FROM permissions WHERE code = 'contest.admin_all';

ALTER TABLE users
    DROP COLUMN session_generation,
    DROP COLUMN must_change_password,
    DROP COLUMN password_changed_at,
    DROP COLUMN last_login_at;
