CREATE TABLE IF NOT EXISTS folders (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id UUID REFERENCES folders(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS folders_unique_active_name_idx
    ON folders (owner_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), LOWER(name))
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS folders_owner_parent_idx
    ON folders (owner_id, parent_id, name)
    WHERE deleted_at IS NULL;

ALTER TABLE files ADD COLUMN IF NOT EXISTS folder_id UUID REFERENCES folders(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS files_owner_folder_idx
    ON files (owner_id, folder_id, created_at DESC)
    WHERE deleted_at IS NULL;
