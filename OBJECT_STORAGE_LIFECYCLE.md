# Object storage lifecycle strategy

Cloudlet does not install an `AbortIncompleteMultipartUpload` bucket lifecycle rule. MinIO does not consistently accept that S3 lifecycle action, while Cloudflare R2 lifecycle support and configuration are provider-specific. Requiring the rule would make startup and cleanup behavior depend on the selected provider.

## Authoritative cleanup

- PostgreSQL `upload_intents` is the source of truth for multipart uploads created by Cloudlet.
- Upload intents expire after 24 hours.
- River claims expired intents hourly with `FOR UPDATE SKIP LOCKED`, releases quota once, and calls the standard S3 `AbortMultipartUpload` API.
- Cleanup claims are retryable. Failed or interrupted aborts remain pending and can be reclaimed after 15 minutes.
- `NoSuchUpload` is treated as successful cleanup, making retries idempotent across MinIO, R2, and AWS S3.
- Bucket objects for active files, versions, thumbnails, and trash are deleted only by Cloudlet's database-backed retention workflows. A broad bucket expiration rule must not be enabled because it could delete live immutable version objects without updating PostgreSQL.

## Provider configuration

Keep the bucket private and enable server-side encryption. Provider-native incomplete multipart lifecycle cleanup may be enabled as optional defense in depth where supported, but Cloudlet must not rely on it and must not attempt to install it during API startup.

For Cloudflare R2, configure any optional lifecycle rule in the R2 dashboard or provider infrastructure code. For MinIO, use Cloudlet's River cleanup worker and do not add the rejected `AbortIncompleteMultipartUpload` rule.
