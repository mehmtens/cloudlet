ALTER TABLE file_versions ADD COLUMN IF NOT EXISTS thumbnail_object_key TEXT;
ALTER TABLE file_versions ADD COLUMN IF NOT EXISTS thumbnail_status TEXT NOT NULL DEFAULT 'UNSUPPORTED';
ALTER TABLE file_versions ADD COLUMN IF NOT EXISTS thumbnail_started_at TIMESTAMPTZ;

ALTER TABLE file_versions DROP CONSTRAINT IF EXISTS file_versions_thumbnail_status_check;
ALTER TABLE file_versions ADD CONSTRAINT file_versions_thumbnail_status_check
    CHECK (thumbnail_status IN ('PENDING','PROCESSING','READY','FAILED','UNSUPPORTED'));

UPDATE file_versions
SET thumbnail_status='PENDING'
WHERE thumbnail_status='UNSUPPORTED'
  AND content_type IN ('image/jpeg','image/png','image/webp','application/pdf');

CREATE INDEX IF NOT EXISTS file_versions_thumbnail_pending_idx
    ON file_versions (created_at) WHERE thumbnail_status='PENDING';
