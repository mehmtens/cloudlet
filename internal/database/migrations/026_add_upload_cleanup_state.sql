ALTER TABLE upload_intents
    ADD COLUMN IF NOT EXISTS storage_cleanup_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS storage_cleanup_claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS storage_cleanup_attempts INTEGER NOT NULL DEFAULT 0;

UPDATE upload_intents
SET storage_cleanup_pending = TRUE
WHERE status = 'EXPIRED' AND storage_upload_id <> '' AND storage_cleanup_pending = FALSE;

CREATE INDEX IF NOT EXISTS upload_intents_storage_cleanup_idx
    ON upload_intents (expires_at)
    WHERE storage_cleanup_pending = TRUE;
