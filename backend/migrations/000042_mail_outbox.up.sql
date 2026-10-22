-- Email waiting to be sent, and the record of what was.
--
-- A request never talks to the relay: it leaves a row here, inside its own
-- transaction, and the API's background sender (internal/mailing) delivers
-- it at the rate the relay allows: five hundred sign-in messages leave over
-- twenty minutes, and no request waits for them.
--
-- The body can carry a one-time password. It is kept only while the message
-- may still be sent: the row that is sent or abandoned loses it, and the
-- table refuses a finished row that still has one.

-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string); five seconds is longer than any query the API may run, so a wait
-- past it is a wait on something else, and the migration gives up rather than
-- queue requests on users behind it.
SET lock_timeout = '5s';

CREATE TABLE mail_outbox (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- mailing.Kind; a metric label, so the code keeps the set fixed.
    kind            text NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 64),
    -- The recipient's account when there is one. The address is copied, not
    -- joined: the message goes where it was meant to when it was queued.
    user_id         uuid REFERENCES users ON DELETE SET NULL,
    -- The bounds are internal/platform/mail's, in bytes as the relay counts.
    recipient       text NOT NULL CHECK (octet_length(recipient) BETWEEN 3 AND 254),
    subject         text NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 512),
    body            text CHECK (octet_length(body) BETWEEN 1 AND 65536),
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Counted when a sender claims the row, so a sender that dies mid-send
    -- still spends one.
    attempts        int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- When the row is next due: on insert, after a claim's lease, or after a
    -- retry's backoff.
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    -- The last claim. The rate limit counts the claims of the last minute
    -- across every replica.
    last_attempt_at timestamptz,
    sent_at         timestamptz,
    abandoned_at    timestamptz,
    -- The last failure, bounded by mailing.MaxLastErrorBytes and never the
    -- body.
    last_error      text CHECK (octet_length(last_error) <= 500),

    CHECK (sent_at IS NULL OR abandoned_at IS NULL),
    CHECK ((body IS NULL) = (sent_at IS NOT NULL OR abandoned_at IS NOT NULL))
);

COMMENT ON TABLE mail_outbox IS
    'Email queued for the relay; the body is erased once the message is sent or abandoned.';

-- The sender's claim and the due-rows gauge: pending rows in due order.
CREATE INDEX mail_outbox_due_idx ON mail_outbox (next_attempt_at, id)
    WHERE sent_at IS NULL AND abandoned_at IS NULL;

-- The rate limit's count of the last minute's claims.
CREATE INDEX mail_outbox_last_attempt_idx ON mail_outbox (last_attempt_at)
    WHERE last_attempt_at IS NOT NULL;

-- The foreign key's: deleting an account would otherwise scan the table.
CREATE INDEX mail_outbox_user_idx ON mail_outbox (user_id)
    WHERE user_id IS NOT NULL;
