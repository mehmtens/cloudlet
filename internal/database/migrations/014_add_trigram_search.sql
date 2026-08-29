CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS files_original_name_trgm_idx
    ON files USING GIN (original_name gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS folders_name_trgm_idx
    ON folders USING GIN (name gin_trgm_ops)
    WHERE deleted_at IS NULL;
