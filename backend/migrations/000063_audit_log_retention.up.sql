-- Lets retention delete audit entries older than eighteen months, and nothing
-- else: the append-only guard opens for a DELETE only when the transaction
-- has set olympdb.audit_retention to 'on' and the row is older than that.
-- UPDATE, TRUNCATE and every younger row stay refused, whatever the setting.
--
-- An exemption rather than ALTER TABLE ... DISABLE TRIGGER: disabling takes a
-- SHARE ROW EXCLUSIVE lock that stops every audited write for the length of
-- the sweep, and needs the table's owner. The exemption takes no table lock
-- and works for any role holding DELETE.
--
-- Eighteen months is the floor of the trail's retention, held here so the
-- database itself refuses a shorter period: the retention period in the code
-- may be longer, never shorter, and a deletion below it is refused as any
-- other rewrite of history is.

-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string); five seconds is longer than any query the API may run, so a wait
-- past it is a wait on something else, and the migration gives up rather than
-- queue requests on the trail behind it.
SET lock_timeout = '5s';

-- One function serves the row trigger and the statement-level TRUNCATE
-- trigger, where OLD is null, so OLD is read only on a DELETE.
CREATE OR REPLACE FUNCTION audit_log_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('olympdb.audit_retention', true) = 'on'
           AND OLD.created_at < now() - interval '18 months' THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only: % refused', TG_OP;
END;
$$;

COMMENT ON FUNCTION audit_log_refuse_change() IS
    'Refuses an UPDATE, DELETE or TRUNCATE on audit_log: the trail is append-only. '
    'Retention alone may DELETE an entry older than 18 months, with olympdb.audit_retention on for its transaction.';
