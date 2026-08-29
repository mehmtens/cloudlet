CREATE TABLE IF NOT EXISTS file_versions (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    object_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    checksum_sha256 TEXT CHECK (checksum_sha256 IS NULL OR checksum_sha256 ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO file_versions (id,file_id,owner_id,object_key,content_type,size_bytes,checksum_sha256,created_at)
SELECT id,id,owner_id,object_key,content_type,size_bytes,checksum_sha256,created_at
FROM files
WHERE owner_id IS NOT NULL
ON CONFLICT DO NOTHING;

ALTER TABLE files ADD COLUMN IF NOT EXISTS current_version_id UUID;
UPDATE files SET current_version_id=id WHERE current_version_id IS NULL AND owner_id IS NOT NULL;
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_current_version_id_fkey;
ALTER TABLE files ADD CONSTRAINT files_current_version_id_fkey
    FOREIGN KEY (current_version_id) REFERENCES file_versions(id);

CREATE OR REPLACE VIEW active_files AS
    SELECT * FROM files WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS file_versions_file_created_idx
    ON file_versions (file_id, created_at DESC);
