# Cloudlet

A lightweight, self-hosted cloud storage service built with Go, PostgreSQL, and S3-compatible object storage.

## Initial roadmap

- File upload and download
- Authentication
- PostgreSQL metadata storage
- S3-compatible object storage
- Presigned URLs
- Multipart uploads
- File sharing, folders, and versioning

### Roadmap status

The initial MVP feature list is implemented. The larger portfolio plan in `outputs/CLOUDLET_PORTFOY_PLANI.md` is not yet complete: the main remaining items are screenshots/demo video and production deployment validation. No single overall percentage is reported until those items are checked off with evidence.

## Local development

Requirements: Go 1.26+ and Docker.

```bash
docker compose --profile dev --env-file .env.docker.example up -d
go run ./cmd/api
```

The API will be available at `http://localhost:18080`. Check it with:

```bash
curl http://localhost:18080/health
```

The Docker web interface serves the bundled, self-contained Swagger UI at `http://localhost:18081/docs/` and redirects `http://localhost:18081/docs` to that canonical URL. The OpenAPI contract is available at `http://localhost:18081/openapi.yaml`. Swagger "Try it out" requests use the same-origin `/api` proxy, include browser cookies, and copy the `XSRF-TOKEN` cookie into `X-CSRF-Token` for state-changing requests. The documentation is public, but it only exposes the API contract. Protected operations still require valid authentication and authorization.

Validate the contract and its exact parity with the registered Go routes from `web/`:

```bash
npm run validate:openapi
```

Register, then upload a file with the returned access token:

```bash
curl -X POST http://localhost:18080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"you@example.com","password":"strong-password"}'

curl -F "file=@example.pdf" \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" \
  http://localhost:18080/v1/files
```

Browser clients may use the `cloudlet_access` httpOnly cookie set by registration/login. For every state-changing cookie-authenticated request, read the `XSRF-TOKEN` cookie and send the same value in the `X-CSRF-Token` header. `GET /v1/auth/csrf` refreshes this token. Bearer-token API clients are exempt from CSRF checks.

Access tokens expire after 15 minutes. Refresh tokens expire after 30 days, are stored only as SHA-256 hashes, and rotate on every `POST /v1/auth/refresh`. Session management endpoints are `GET /v1/auth/sessions`, `DELETE /v1/auth/sessions/{id}`, `POST /v1/auth/logout`, and `POST /v1/auth/logout-all`.

Browser authentication is cookie-only: access and refresh tokens are stored in HttpOnly cookies and are not returned in JSON responses. Refresh and logout require the refresh cookie plus CSRF validation.

TOTP seeds are encrypted with AES-256-GCM using `TOTP_ENCRYPTION_KEY`, with the user ID bound as authenticated data. The encryption key must be a base64-encoded 32-byte secret kept outside PostgreSQL.

Registration sends a verification email containing a single-use, 24-hour token. Only its SHA-256 hash is stored. The SPA consumes the `?verify=` link through `POST /v1/auth/email/verify`; authenticated users can inspect status or request a replacement email with `/v1/auth/email/status` and `/v1/auth/email/resend`. Creating public share links requires a verified address. Local Compose uses Mailpit, whose inbox is available at `http://localhost:18025`.

Production does not use Mailpit. Copy `.env.production.example` to an ignored `.env.production` file and configure a real SMTP provider, a sender address on a verified domain, and the public HTTPS Cloudlet URL. Production startup fails closed when SMTP credentials, STARTTLS, secure cookies, or an HTTPS public URL are missing, so a deployment cannot silently run with test email delivery.

Password recovery uses the same production email channel. `POST /v1/auth/password/forgot` returns an identical `202` response for known and unknown addresses, preventing account enumeration. Reset links expire after 30 minutes, are stored only as SHA-256 hashes, are single-use, require a 12-character password, and revoke every existing refresh session after a successful reset.

TOTP authenticator setup is available through the authenticated `GET /v1/auth/totp`, `POST /v1/auth/totp/setup`, `POST /v1/auth/totp/enable`, and `POST /v1/auth/totp/disable` endpoints. Setup returns a standard `otpauth://` URI; the code must be confirmed before the second factor is enabled. Once enabled, login requires the optional `totp_code` field in addition to the password.

Files can be shared directly with another registered user via `POST /v1/files/{id}/access` using `email` and `permission` (`read` or `write`). Recipients see their files through `GET /v1/shared-with-me` and receive short-lived presigned downloads through the dedicated shared-download endpoint.
Owners can update or revoke a grant with `PATCH` or `DELETE /v1/file-access/{id}`.

Authenticated users can change their password with `POST /v1/auth/password/change` (`current_password` and `password`). The current password is verified with bcrypt, the new password must be at least 12 characters, and all refresh sessions are revoked after a successful change.

Users can permanently close their account with `DELETE /v1/auth/account`. The operation requires the current password, aborts pending multipart uploads, deletes every stored version and thumbnail, then removes the PostgreSQL user and all cascading metadata. If object cleanup fails, the database account is retained.

List files and create a 15-minute download link:

```bash
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/files
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/files/{file-id}/download
```

Search active files and folders with PostgreSQL trigram indexes:

```bash
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" "http://localhost:18080/v1/search?q=report&limit=20"
```

File lists support `sort=name|size|created_at`, `order=asc|desc`, `content_type`, `min_size`, and `max_size` query parameters.

Large uploads use `POST /v1/uploads`, direct presigned part uploads, and `POST /v1/uploads/{id}/complete`. Upload intents reserve quota for 24 hours, are idempotent per user-provided key, use 8 MiB parts, and may be cancelled with `DELETE /v1/uploads/{id}`.
An interrupted pending upload can query `GET /v1/uploads/{id}` to receive fresh part URLs and the already completed parts, then continue with only the missing parts before calling the complete endpoint.

The `web/` directory contains the React/Vite SPA. Run `npm install && npm run dev` inside it for local development; Vite proxies `/api` to the Go API. For the single-origin deployment, build the SPA and run Caddy with the root `Caddyfile`; `/api/*` is reverse-proxied to the backend and cookies remain same-origin.

The Playwright suite exercises registration, folder creation, API-backed upload, trash/restore, and secure account cleanup against the running Docker application. Install Chromium once with `cd web && npx playwright install chromium`, then run `npm run test:e2e` while Cloudlet is available at `http://localhost:18081`.

The API contract is documented in [`openapi.yaml`](openapi.yaml) and can be loaded into Swagger UI or another OpenAPI client.

The system flow, storage boundaries, and security boundaries are documented in [`ARCHITECTURE.md`](ARCHITECTURE.md). The migration-derived PostgreSQL model, relationship cardinalities, deletion behavior, and data dictionary are documented in [`ER_DIAGRAM.md`](ER_DIAGRAM.md).

GitHub Actions runs backend tests/vet and the Vite production build. Prometheus is included in Compose on port `19090` and scrapes the API metrics endpoint; Grafana is available on port `13000` with a pre-provisioned Cloudlet dashboard.

PostgreSQL/S3 integration tests use real PostgreSQL and MinIO services. With the Docker Compose dependencies running locally, execute `CLOUDLET_INTEGRATION_TESTS=1 go test -count=1 -v ./internal/integration` (PowerShell: `$env:CLOUDLET_INTEGRATION_TESTS='1'; go test -count=1 -v ./internal/integration`). CI starts isolated PostgreSQL and MinIO services and runs the same suite.

The backend image uses a multi-stage Docker build, runs as an unprivileged user, and includes Poppler for PDF thumbnails. The web image builds the React SPA and serves it with Caddy. `docker compose --profile dev --env-file .env.docker.example up -d --build` starts the complete local application on port `18081`, plus PostgreSQL, MinIO, Mailpit, Prometheus, and Grafana. The API waits for PostgreSQL and MinIO health before starting, and the web container waits for the API liveness check. For local host development use `.env.example`.

`S3_ENDPOINT` is the private backend-to-storage address. `S3_PUBLIC_ENDPOINT` is the browser-reachable address embedded in short-lived presigned upload and download URLs; set it to the public S3/R2 endpoint in deployment.

Some S3-compatible providers do not implement `PutBucketCors`; Cloudlet treats that optional setup response as non-fatal so the API remains available. Direct browser multipart uploads still require the configured public endpoint to allow the origin in its own CORS policy.

The dashboard supports drag-and-drop uploads and tracks upload progress through `XMLHttpRequest`, while preserving the API's cookie/CSRF security model. If a browser cannot reach the public S3 endpoint for a multipart upload, it automatically falls back to the authenticated API upload path.

When `CLAMAV_ADDRESS` is configured, API uploads and version uploads are spooled to a temporary file and scanned through ClamAV before they are written to object storage. Direct multipart uploads are scanned immediately after multipart completion and before file metadata/quota finalization; malware or scanner errors fail closed and the completed object is removed. Production requires `CLAMAV_ADDRESS`.

`CLAMAV_SCAN_TIMEOUT` controls the application-side deadline. The bundled ClamAV image sets `StreamMaxLength` through `CLAMAV_STREAM_MAX_LENGTH` (110 MiB by default); keep it larger than `MAX_UPLOAD_BYTES` when changing upload limits.

Production requires `S3_SERVER_SIDE_ENCRYPTION` (`AES256` or `aws:kms`). The selected S3 server-side encryption is applied to both regular and multipart uploads; `S3_KMS_KEY_ID` is supported when using a customer-managed KMS key.

Background work uses River on the existing PostgreSQL database, so no Redis service is required. The expired-upload cleanup worker runs hourly, uses `FOR UPDATE SKIP LOCKED`, releases reservations, and aborts abandoned S3 multipart uploads. Version retention runs every 15 minutes. A daily retention job permanently removes files and folder trees that have remained in trash for 30 days, deletes all their version objects, and releases their quota.

Object-storage lifecycle is application-managed for MinIO/R2 portability. Failed multipart aborts remain retryable, interrupted cleanup claims are reclaimed, and `NoSuchUpload` is handled idempotently. Cloudlet deliberately does not install the MinIO-incompatible `AbortIncompleteMultipartUpload` bucket rule. See [`OBJECT_STORAGE_LIFECYCLE.md`](OBJECT_STORAGE_LIFECYCLE.md).

Each file has immutable objects in `file_versions`. Use `POST /v1/files/{id}/versions` to upload a new version, `GET /v1/files/{id}/versions` for history, and the version-specific download/restore endpoints. Every stored version consumes quota; selecting an older version as current does not duplicate its object. Cloudlet keeps at most 20 versions per active file, always preserves the current version, deletes pruned objects, and returns their bytes to the owner's quota. Files in trash are not pruned before trash retention handles them.

An older version can be permanently removed with `DELETE /v1/files/{id}/versions/{version_id}`; the current version is protected and returns `409`.

JPEG, PNG, WebP, and PDF uploads receive asynchronous 320x240-bounded JPEG thumbnails through River; PDF thumbnails render the first page with Poppler (`PDFTOPPM_PATH`). `GET /v1/files/{id}/thumbnail` checks ownership before returning a 15-minute presigned URL. Thumbnail objects remain private under `users/{user-id}/files/{file-id}/thumbnails/{version-id}.jpg`.

Rename a file or move it to the trash:

```bash
curl -X PATCH -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"renamed.pdf"}' http://localhost:18080/v1/files/{file-id}
curl -X DELETE -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/files/{file-id}
```

Move one file or up to 100 files atomically between owned folders. Sending `folder_id: null` moves files to the root; a destination owned by another user is rejected.

```bash
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"folder_id":"{folder-id}"}' http://localhost:18080/v1/files/{file-id}/move
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"ids":["{file-id-1}","{file-id-2}"],"folder_id":null}' http://localhost:18080/v1/files/bulk/move
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"ids":["{file-id-1}","{file-id-2}"]}' http://localhost:18080/v1/files/bulk/trash
```

List the trash, restore a file, or permanently delete it:

```bash
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash/{file-id}/restore
curl -X DELETE -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash/{file-id}
```

Create nested folders and upload into one:

```bash
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Projects","parent_id":null}' http://localhost:18080/v1/folders
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" "http://localhost:18080/v1/folders?parent_id={folder-id}"
curl -F "file=@example.pdf" -F "folder_id={folder-id}" \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/files
```

Rename or move a folder (use `null` to move it to the root):

```bash
curl -X PATCH -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Archive"}' http://localhost:18080/v1/folders/{folder-id}
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"parent_id":null}' http://localhost:18080/v1/folders/{folder-id}/move
```

Move a folder tree to the trash, restore it, or permanently delete the tree and its objects:

```bash
curl -X DELETE -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/folders/{folder-id}
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash/folders
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash/folders/{folder-id}/restore
curl -X DELETE -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/trash/folders/{folder-id}
```

Inspect the authenticated user's 5 GiB storage quota:

```bash
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/storage
```

Trash contents continue to consume quota until they are permanently deleted. Uploads that would exceed the quota return `409 storage_quota_exceeded`.

Create and manage a public share link (verified email required):

```bash
curl -X POST -H "Authorization: Bearer YOUR_ACCESS_TOKEN" -H "Content-Type: application/json" \
  -d '{"password":"optional-secret","expires_at":"2026-09-01T12:00:00Z","max_access_starts":10}' \
  http://localhost:18080/v1/files/{file-id}/shares
curl -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/files/{file-id}/shares
curl -X DELETE -H "Authorization: Bearer YOUR_ACCESS_TOKEN" http://localhost:18080/v1/shares/{share-id}
```

Exchange the public token for a three-minute presigned URL:

```bash
curl -X POST -H "Content-Type: application/json" -d '{"password":"optional-secret"}' \
  http://localhost:18080/v1/public/shares/{token}/download
```

The counter represents **download/access starts**, not completed transfers, and increments when a presigned URL is issued.
