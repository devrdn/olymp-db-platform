-- The public leaderboard is read without a session, and an account made by
-- the public registration form takes its lowercased email address as its
-- login. A contest showing logins with such a participant publishes a private
-- mailbox address, so it shows full names instead. A contest whose
-- participants all have card-number or username logins keeps the organiser's
-- choice, and an archived one is no longer served.
--
-- updated_at stays as it is: the reclaim sweep measures a finished contest's
-- grace from it.
SET lock_timeout = '5s';

UPDATE contests c
SET leaderboard_names = 'full_name'
WHERE c.leaderboard_names = 'login'
  AND c.status <> 'archived'
  AND EXISTS (
      SELECT 1
      FROM registrations r
      JOIN users u ON u.id = r.user_id
      WHERE r.contest_id = c.id
        AND u.status <> 'deleted'
        AND u.login = lower(u.email)
  );
