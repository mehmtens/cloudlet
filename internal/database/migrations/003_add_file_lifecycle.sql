ALTER TABLE files ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS files_owner_active_created_at_idx
    ON files (owner_id, created_at DESC)
    WHERE deleted_at IS NULL;
