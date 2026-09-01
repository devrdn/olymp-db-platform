-- The images an installation puts on itself.
--
-- In the database rather than on a volume, and that is a deployment decision
-- rather than a preference. `make backup` dumps the core database and
-- `make restore-check` proves the dump loads; a directory beside it would be a
-- second thing to back up and the one nobody remembers until a restore comes
-- back branded as nothing. These are a few hundred kilobytes in total, read
-- once and then cached by content hash, so the usual argument against blobs
-- does not reach them.
CREATE TABLE settings_files (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- What the image is for. One row per purpose: replacing the logo replaces
    -- the row, so there is no gallery of abandoned uploads to clean up.
    kind         text NOT NULL UNIQUE
        CHECK (kind IN ('logo', 'icon', 'favicon')),
    -- Determined by reading the bytes, never from the upload's own claim.
    content_type text NOT NULL,
    bytes        bytea NOT NULL,
    -- The URL carries this, so a replaced image is a different address and
    -- caches never have to be told anything.
    sha256       text NOT NULL,
    width        int  NOT NULL,
    height       int  NOT NULL,
    uploaded_at  timestamptz NOT NULL DEFAULT now(),
    uploaded_by  uuid REFERENCES users ON DELETE SET NULL
);
