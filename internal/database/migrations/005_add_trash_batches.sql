ALTER TABLE folders ADD COLUMN IF NOT EXISTS trash_batch_id UUID;
ALTER TABLE files ADD COLUMN IF NOT EXISTS trash_batch_id UUID;

CREATE INDEX IF NOT EXISTS folders_trash_batch_idx ON folders (owner_id, trash_batch_id)
    WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS files_trash_batch_idx ON files (owner_id, trash_batch_id)
    WHERE deleted_at IS NOT NULL;
