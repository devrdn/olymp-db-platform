-- Whoever has managed a contest has read its reference answers and the
-- unfrozen leaderboard, so revoking the role must not make them a
-- participant. The table keeps who was revoked; the backfill recovers the
-- revokes the append-only audit trail already holds.
SET lock_timeout = '5s';

CREATE TABLE contest_former_managers (
    contest_id uuid NOT NULL REFERENCES contests (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    revoked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contest_id, user_id)
);

INSERT INTO contest_former_managers (contest_id, user_id, revoked_at)
SELECT a.entity_id::uuid, (a.payload->>'user_id')::uuid, min(a.created_at)
FROM audit_log a
WHERE a.action = 'contest.manager_revoke'
  AND a.payload->>'user_id' IS NOT NULL
  AND EXISTS (SELECT 1 FROM contests c WHERE c.id = a.entity_id::uuid)
  AND EXISTS (SELECT 1 FROM users u WHERE u.id = (a.payload->>'user_id')::uuid)
GROUP BY a.entity_id::uuid, (a.payload->>'user_id')::uuid
ON CONFLICT DO NOTHING;
