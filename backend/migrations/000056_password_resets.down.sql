-- Discards every reset link not yet followed; each person asks for another.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS password_resets;
