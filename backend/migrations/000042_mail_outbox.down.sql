-- Drops the queue with whatever it still holds: messages not yet sent are
-- lost, and their one-time passwords with them.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS mail_outbox;
