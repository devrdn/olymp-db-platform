-- Discards every registration and its code; they are not archived.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS event_registrations;
