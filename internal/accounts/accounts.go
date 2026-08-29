package accounts

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store interface {
	Delete(context.Context, string) error
	AbortMultipart(context.Context, string, string) error
}

type Service struct {
	db    *pgxpool.Pool
	store Store
}

func NewService(db *pgxpool.Pool, store Store) *Service {
	return &Service{db: db, store: store}
}

// Close permanently removes every stored object before deleting the user row.
// Locking the user serializes closure with quota-reserving upload operations.
func (s *Service) Close(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&exists); err != nil {
		return err
	}
	keys, err := accountObjectKeys(ctx, tx, userID)
	if err != nil {
		return err
	}
	pending, err := pendingUploads(ctx, tx, userID)
	if err != nil {
		return err
	}
	for _, upload := range pending {
		if err = s.store.AbortMultipart(ctx, upload.objectKey, upload.uploadID); err != nil {
			return fmt.Errorf("abort pending upload: %w", err)
		}
	}
	for _, key := range keys {
		if err = s.store.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete account object: %w", err)
		}
	}
	if tag, deleteErr := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID); deleteErr != nil {
		return deleteErr
	} else if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

func accountObjectKeys(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT object_key FROM file_versions WHERE owner_id=$1
		UNION SELECT thumbnail_object_key FROM file_versions WHERE owner_id=$1 AND thumbnail_object_key IS NOT NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

type pendingUpload struct{ objectKey, uploadID string }

func pendingUploads(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]pendingUpload, error) {
	rows, err := tx.Query(ctx, `SELECT object_key,storage_upload_id FROM upload_intents WHERE owner_id=$1 AND status='PENDING'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []pendingUpload{}
	for rows.Next() {
		var upload pendingUpload
		if err := rows.Scan(&upload.objectKey, &upload.uploadID); err != nil {
			return nil, err
		}
		result = append(result, upload)
	}
	return result, rows.Err()
}
