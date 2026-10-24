-- Discards every question, answer and announcement; they are not archived.
SET lock_timeout = '5s';

DELETE FROM role_permissions
WHERE permission_id = (SELECT id FROM permissions WHERE code = 'contest.clarify');

DELETE FROM permissions WHERE code = 'contest.clarify';

DROP TABLE IF EXISTS announcements;
DROP TABLE IF EXISTS clarifications;

ALTER TABLE questions DROP CONSTRAINT IF EXISTS questions_id_contest_key;
