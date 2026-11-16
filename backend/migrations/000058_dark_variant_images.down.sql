-- Discards the dark-theme variants; the slots no longer exist afterwards.
SET lock_timeout = '5s';

DELETE FROM settings_files WHERE kind IN ('logo-dark', 'favicon-dark');
ALTER TABLE settings_files DROP CONSTRAINT settings_files_kind_check;
ALTER TABLE settings_files ADD CONSTRAINT settings_files_kind_check
    CHECK (kind IN ('logo', 'icon', 'favicon'));
