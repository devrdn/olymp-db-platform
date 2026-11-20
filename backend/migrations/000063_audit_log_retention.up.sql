-- Lets retention delete audit entries older than eighteen months, and nothing
-- else: the append-only guard opens for a DELETE only when the setting
-- olympdb.audit_retention holds the current transaction's own id and the row
-- is older than that. UPDATE, TRUNCATE and every younger row stay refused,
-- whatever the setting.
--
-- The caller opens it for one transaction, inside it:
--     SELECT set_config('olympdb.audit_retention', pg_current_xact_id()::text, true);
-- Naming the transaction, not a bare 'on', means a value left on a pooled
-- connection by a session-wide SET matches no later transaction.
--
-- An exemption rather than ALTER TABLE ... DISABLE TRIGGER: disabling takes a
-- SHARE ROW EXCLUSIVE lock that stops every audited write for the length of
-- the sweep, and needs the table's owner. The exemption takes no table lock
-- and works for any role holding DELETE.
--
-- Eighteen months is the floor of the trail's retention, held here so the
-- database itself refuses a shorter period: internal/retention.AuditLogMonths
-- may be longer, never shorter, and a deletion below the floor is refused as
-- any other rewrite of history is.
SET lock_timeout = '5s';

-- One function serves the row trigger and the statement-level TRUNCATE
-- trigger, where OLD is null, so OLD is read only on a DELETE.
CREATE OR REPLACE FUNCTION audit_log_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('olympdb.audit_retention', true) = pg_current_xact_id()::text
           AND OLD.created_at < now() - interval '18 months' THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only: % refused', TG_OP;
END;
$$;

-- The guard decides a security rule; no schema a caller puts first on its
-- search_path may stand in for the functions it calls.
ALTER FUNCTION audit_log_refuse_change() SET search_path = pg_catalog;

COMMENT ON FUNCTION audit_log_refuse_change() IS
    'Refuses an UPDATE, DELETE or TRUNCATE on audit_log: the trail is append-only. '
    'Retention alone may DELETE an entry older than 18 months, with olympdb.audit_retention set to its transaction''s id.';
