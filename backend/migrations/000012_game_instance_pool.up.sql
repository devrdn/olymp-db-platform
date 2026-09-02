-- A spare copy and a participant's instance are the same thing: a database
-- provisioned from one version of a template. The only difference is whether
-- anybody owns it yet.
--
-- Modelling them as two tables would make claiming a copy two writes — delete
-- here, insert there — where it should be one. As one table with an owner that
-- may be absent, claiming is a single UPDATE that either wins or finds nothing,
-- which is exactly what two late registrations racing for the last spare copy
-- need. Invalidating a stale template version and reporting the depth of the
-- pool both become one query for the same reason.

ALTER TABLE game_instances ALTER COLUMN registration_id DROP NOT NULL;

-- A spare copy belongs to a contest before it belongs to anybody, so the
-- contest can no longer be reached through the registration.
ALTER TABLE game_instances ADD COLUMN contest_id uuid;

UPDATE game_instances i
SET contest_id = r.contest_id
FROM registrations r
WHERE r.id = i.registration_id;

DELETE FROM game_instances WHERE contest_id IS NULL;

ALTER TABLE game_instances ALTER COLUMN contest_id SET NOT NULL;

-- The registration reference becomes composite, which is what makes it
-- impossible to hand a contest's spare copy to somebody registered for a
-- different contest. A plain foreign key on registration_id alone cannot say
-- that, and a check somewhere in the application is a check that is one day
-- forgotten.
ALTER TABLE registrations ADD CONSTRAINT registrations_id_contest_key UNIQUE (id, contest_id);

ALTER TABLE game_instances DROP CONSTRAINT game_instances_registration_id_fkey;

ALTER TABLE game_instances
    ADD CONSTRAINT game_instances_contest_fkey
        FOREIGN KEY (contest_id) REFERENCES contests ON DELETE CASCADE,
    ADD CONSTRAINT game_instances_registration_fkey
        FOREIGN KEY (registration_id, contest_id)
            REFERENCES registrations (id, contest_id) ON DELETE CASCADE;

-- Serves the claim: the next free copy of the current template version for a
-- contest. Partial, because free copies are the small minority of rows and the
-- claim never looks at the others.
CREATE INDEX game_instances_free_idx
    ON game_instances (contest_id, template_version)
    WHERE registration_id IS NULL AND status = 'ready';

-- Serves the invalidation sweep and the "are all instances current" gate that
-- a contest must pass before it may start.
CREATE INDEX game_instances_contest_version_idx ON game_instances (contest_id, template_version);
