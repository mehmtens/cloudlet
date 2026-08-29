DO $$ BEGIN
    CREATE TYPE upload_intent_status AS ENUM ('PENDING','COMPLETED','EXPIRED','CANCELLED');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS upload_intents (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id UUID REFERENCES folders(id) ON DELETE SET NULL,
    file_id UUID NOT NULL,
    object_key TEXT NOT NULL UNIQUE,
    storage_upload_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    original_name TEXT NOT NULL,
    content_type TEXT NOT NULL,
    expected_size BIGINT NOT NULL CHECK (expected_size > 0),
    checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    part_size BIGINT NOT NULL CHECK (part_size >= 5242880),
    status upload_intent_status NOT NULL DEFAULT 'PENDING',
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS upload_intents_expiry_idx
    ON upload_intents (expires_at)
    WHERE status = 'PENDING';
