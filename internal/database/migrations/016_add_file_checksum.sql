ALTER TABLE files ADD COLUMN IF NOT EXISTS checksum_sha256 TEXT;
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_checksum_sha256_format;
ALTER TABLE files ADD CONSTRAINT files_checksum_sha256_format
    CHECK (checksum_sha256 IS NULL OR checksum_sha256 ~ '^[0-9a-f]{64}$');

CREATE OR REPLACE VIEW active_files AS
    SELECT * FROM files WHERE deleted_at IS NULL;
