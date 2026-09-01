DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'settings.manage');

DELETE FROM permissions WHERE code = 'settings.manage';

DROP TABLE IF EXISTS settings;
