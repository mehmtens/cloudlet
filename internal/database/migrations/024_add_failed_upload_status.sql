ALTER TYPE upload_intent_status ADD VALUE IF NOT EXISTS 'FAILED';

ALTER TABLE upload_intents
    DROP CONSTRAINT IF EXISTS upload_intents_owner_id_idempotency_key_key;

CREATE UNIQUE INDEX IF NOT EXISTS upload_intents_active_idempotency_idx
    ON upload_intents (owner_id, idempotency_key)
    WHERE status IN ('PENDING', 'COMPLETED');
