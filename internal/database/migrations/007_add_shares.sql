CREATE TABLE IF NOT EXISTS shares (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    password_hash TEXT,
    expires_at TIMESTAMPTZ,
    max_downloads BIGINT CHECK (max_downloads IS NULL OR max_downloads > 0),
    download_count BIGINT NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shares_owner_file_idx ON shares (owner_id, file_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shares_active_expiry_idx ON shares (expires_at) WHERE revoked_at IS NULL;
