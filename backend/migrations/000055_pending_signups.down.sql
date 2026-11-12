-- Discards every sign-up still waiting for its code; nobody can confirm one
-- afterwards, and each person signs up again.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS pending_signups;
