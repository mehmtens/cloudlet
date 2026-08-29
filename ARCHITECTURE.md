# Cloudlet architecture

## Request and storage flow

```text
Browser / SPA
     │ same-origin cookies + CSRF
     ▼
Caddy reverse proxy
     │ /api/*
     ▼
Go HTTP API ───────────────► PostgreSQL
     │ metadata, auth, quota, audit
     │
     ├── small upload ─────► S3-compatible object storage
     ├── multipart intent ─► presigned S3 parts
     └── presigned download/thumbnail URL

PostgreSQL ── River workers
                 ├─ expired multipart cleanup
                 ├─ 20-version retention
                 ├─ 30-day trash retention
                 └─ image/PDF thumbnail generation
```

## Security boundaries

- The bucket is private; clients never receive storage credentials.
- API access is authorized from the authenticated owner, never from a request body user ID.
- Browser mutations require the Double-Submit CSRF cookie/header pair.
- Object keys are UUID-based and contain no user-controlled path fragments.
- File metadata is isolated by owner UUID and active views exclude soft-deleted records.
- Download and thumbnail access uses short-lived presigned URLs.
- Audit events are append-only and retention jobs run through the PostgreSQL-backed River queue.

## Data ownership

PostgreSQL stores identity, hierarchy, permissions, quotas, checksums, versions, and thumbnail references. S3/MinIO stores immutable file and thumbnail bytes. A version row is the source of truth for each object; deleting a version deletes its object and returns its bytes to the owner quota.
