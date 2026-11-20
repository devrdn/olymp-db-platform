-- Closes the retention exemption: the guard refuses every DELETE again.
SET lock_timeout = '5s';

CREATE OR REPLACE FUNCTION audit_log_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % refused', TG_OP;
END;
$$;

COMMENT ON FUNCTION audit_log_refuse_change() IS
    'Refuses an UPDATE, DELETE or TRUNCATE on audit_log: the trail is append-only.';
