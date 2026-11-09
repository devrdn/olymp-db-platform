-- A login equal to the account's lowercased email is the public registration
-- form's, so a contest showing logins with such a participant publishes a
-- mailbox address; updated_at stays, as the reclaim sweep measures a finished
-- contest's grace from it.
SET lock_timeout = '5s';

UPDATE contests c
SET leaderboard_names = 'full_name'
WHERE c.leaderboard_names = 'login'
  AND EXISTS (
      SELECT 1
      FROM registrations r
      JOIN users u ON u.id = r.user_id
      WHERE r.contest_id = c.id
        AND u.status <> 'deleted'
        AND u.login = lower(u.email)
  );
