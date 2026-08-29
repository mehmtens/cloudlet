CREATE TABLE IF NOT EXISTS file_access_grants (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    grantee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission TEXT NOT NULL DEFAULT 'read' CHECK (permission IN ('read','write')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (file_id, grantee_id),
    CHECK (owner_id <> grantee_id)
);
CREATE INDEX IF NOT EXISTS file_access_grants_grantee_idx ON file_access_grants (grantee_id, created_at DESC);
