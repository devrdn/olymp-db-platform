-- The audit trail is append-only, and from here on the database says so, not
-- only the code. Nothing in the service updates or deletes a row of
-- audit_log; this trigger refuses either, so a compromised application
-- credential or a future injection cannot rewrite history with a statement.
-- The owner can still drop the trigger, which is a visible DDL step rather
-- than a quiet UPDATE: retention, if it is ever needed, runs as the owner
-- after dropping it.

-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string); five seconds is longer than any query the API may run, so a wait
-- past it is a wait on something else, and the migration gives up rather than
-- queue requests on the trail behind it.
SET lock_timeout = '5s';

CREATE FUNCTION audit_log_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % refused', TG_OP;
END;
$$;

COMMENT ON FUNCTION audit_log_refuse_change() IS
    'Refuses an UPDATE, DELETE or TRUNCATE on audit_log: the trail is append-only.';

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_refuse_change();

-- TRUNCATE fires no row trigger, so it takes a statement trigger of its own.
CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_refuse_change();
