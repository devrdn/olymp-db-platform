-- No route checks reports.view, and contest roles grant what they grant from
-- code, so the permission separates nobody.
SET lock_timeout = '5s';
DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'reports.view');
DELETE FROM permissions WHERE code = 'reports.view';
