package database

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_create_files.sql
var initialMigration string

//go:embed migrations/002_add_users.sql
var usersMigration string

//go:embed migrations/003_add_file_lifecycle.sql
var fileLifecycleMigration string

//go:embed migrations/004_add_folders.sql
var foldersMigration string

//go:embed migrations/005_add_trash_batches.sql
var trashBatchesMigration string

//go:embed migrations/006_add_storage_quotas.sql
var storageQuotasMigration string

//go:embed migrations/007_add_shares.sql
var sharesMigration string

//go:embed migrations/008_add_email_verification.sql
var emailVerificationMigration string

//go:embed migrations/009_rename_share_counters.sql
var shareCounterMigration string

//go:embed migrations/010_add_auth_sessions.sql
var authSessionsMigration string

//go:embed migrations/011_add_active_file_name_constraint.sql
var activeFileNameMigration string

//go:embed migrations/012_add_active_views.sql
var activeViewsMigration string

//go:embed migrations/013_add_folder_materialized_path.sql
var folderMaterializedPathMigration string

//go:embed migrations/014_add_trigram_search.sql
var trigramSearchMigration string

//go:embed migrations/015_add_audit_log.sql
var auditLogMigration string

//go:embed migrations/016_add_file_checksum.sql
var fileChecksumMigration string

//go:embed migrations/017_add_upload_intents.sql
var uploadIntentsMigration string

//go:embed migrations/018_add_file_versions.sql
var fileVersionsMigration string

//go:embed migrations/019_add_thumbnails.sql
var thumbnailsMigration string

//go:embed migrations/020_add_email_verification_tokens.sql
var emailVerificationTokensMigration string

//go:embed migrations/021_add_password_reset_tokens.sql
var passwordResetTokensMigration string

//go:embed migrations/022_add_file_access_grants.sql
var fileAccessGrantsMigration string

//go:embed migrations/023_add_totp_auth.sql
var totpAuthMigration string

//go:embed migrations/024_add_failed_upload_status.sql
var failedUploadMigration string

//go:embed migrations/025_preserve_audit_actor_on_user_delete.sql
var preserveAuditActorMigration string

//go:embed migrations/026_add_upload_cleanup_state.sql
var uploadCleanupStateMigration string

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	if _, err := db.Exec(ctx, initialMigration); err != nil {
		return err
	}
	_, err := db.Exec(ctx, usersMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, fileLifecycleMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, foldersMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, trashBatchesMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, storageQuotasMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, sharesMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, emailVerificationMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, shareCounterMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, authSessionsMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, activeFileNameMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, activeViewsMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, folderMaterializedPathMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, trigramSearchMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, auditLogMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, fileChecksumMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, uploadIntentsMigration)
	if err != nil {
		return err
	}
	if _, err = db.Exec(ctx, fileVersionsMigration); err != nil {
		return err
	}
	if _, err = db.Exec(ctx, thumbnailsMigration); err != nil {
		return err
	}
	_, err = db.Exec(ctx, emailVerificationTokensMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, passwordResetTokensMigration)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, fileAccessGrantsMigration)
	if err != nil {
		return err
	}
	if _, err = db.Exec(ctx, totpAuthMigration); err != nil {
		return err
	}
	if _, err = db.Exec(ctx, failedUploadMigration); err != nil {
		return err
	}
	if _, err = db.Exec(ctx, preserveAuditActorMigration); err != nil {
		return err
	}
	_, err = db.Exec(ctx, uploadCleanupStateMigration)
	return err
}
