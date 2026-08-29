ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_bytes BIGINT NOT NULL DEFAULT 5368709120;
ALTER TABLE users ADD COLUMN IF NOT EXISTS used_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS reserved_bytes BIGINT NOT NULL DEFAULT 0;

UPDATE users u SET used_bytes = COALESCE((
    SELECT SUM(f.size_bytes) FROM files f WHERE f.owner_id = u.id
), 0);

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_storage_nonnegative;
ALTER TABLE users ADD CONSTRAINT users_storage_nonnegative
    CHECK (quota_bytes >= 0 AND used_bytes >= 0 AND reserved_bytes >= 0);
