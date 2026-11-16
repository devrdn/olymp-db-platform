-- Two more image slots: the dark-theme variants of the logo and the tab icon.
-- The constraint was created inline in 000010, so it carries the default name.
SET lock_timeout = '5s';

ALTER TABLE settings_files DROP CONSTRAINT settings_files_kind_check;
ALTER TABLE settings_files ADD CONSTRAINT settings_files_kind_check
    CHECK (kind IN ('logo', 'logo-dark', 'icon', 'favicon', 'favicon-dark'));
