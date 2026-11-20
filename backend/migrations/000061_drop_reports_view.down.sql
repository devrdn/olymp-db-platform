-- Restores the seeded state (admin only); grants a data-made role held are not
-- recorded anywhere.
SET lock_timeout = '5s';
INSERT INTO permissions (code, name)
VALUES ('reports.view', 'View contest reports and the query journal')
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.code = 'reports.view'
WHERE r.code = 'admin'
ON CONFLICT DO NOTHING;
