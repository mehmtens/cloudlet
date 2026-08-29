package uploads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"

	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/scanning"
	"github.com/mehmtens/cloudlet/internal/storage"
)

const MinPartSize int64 = 5 << 20
const DefaultPartSize int64 = 8 << 20

var ErrInvalidUpload = errors.New("invalid multipart upload")
var ErrUploadConflict = errors.New("upload is not pending")
var ErrSizeMismatch = errors.New("completed object size does not match expected size")

type Intent struct {
	ID, OwnerID, FileID                                                                           uuid.UUID
	FolderID                                                                                      *uuid.UUID
	ObjectKey, StorageUploadID, IdempotencyKey, OriginalName, ContentType, ChecksumSHA256, Status string
	ExpectedSize, PartSize                                                                        int64
	ExpiresAt, CreatedAt                                                                          time.Time
}
type PartURL struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
}
type Started struct {
	IntentID  uuid.UUID               `json:"intent_id"`
	FileID    uuid.UUID               `json:"file_id"`
	PartSize  int64                   `json:"part_size"`
	ExpiresAt time.Time               `json:"expires_at"`
	Parts     []PartURL               `json:"parts"`
	Completed []storage.CompletedPart `json:"completed_parts,omitempty"`
}
type Start struct {
	Name, ContentType, ChecksumSHA256, IdempotencyKey string
	FolderID                                          *uuid.UUID
	Size                                              int64
}

type Store interface {
	CreateMultipart(context.Context, string, string) (string, error)
	PresignUploadPart(context.Context, string, string, int32, time.Duration) (string, error)
	CompleteMultipart(context.Context, string, string, []storage.CompletedPart) error
	AbortMultipart(context.Context, string, string) error
	ObjectSize(context.Context, string) (int64, error)
	ListParts(context.Context, string, string) ([]storage.CompletedPart, error)
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

type Service struct {
	repo    *Repository
	store   Store
	maxSize int64
	scanner scanning.Scanner
}

func NewService(repo *Repository, store Store, maxSize int64, scanners ...scanning.Scanner) *Service {
	var scanner scanning.Scanner
	if len(scanners) > 0 {
		scanner = scanners[0]
	}
	return &Service{repo: repo, store: store, maxSize: maxSize, scanner: scanner}
}

func (s *Service) Start(ctx context.Context, ownerID uuid.UUID, input Start) (Started, error) {
	input.Name = norm.NFC.String(strings.TrimSpace(input.Name))
	input.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(input.ChecksumSHA256))
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := files.ValidateContentType(input.ContentType, input.ContentType); err != nil {
		return Started{}, err
	}
	if input.Name == "" || input.Size <= 0 || input.Size > s.maxSize || len(input.ChecksumSHA256) != 64 || input.IdempotencyKey == "" {
		return Started{}, ErrInvalidUpload
	}
	fileID, intentID := uuid.New(), uuid.New()
	key := "users/" + ownerID.String() + "/files/" + fileID.String() + "/versions/" + uuid.NewString()
	storageID, err := s.store.CreateMultipart(ctx, key, input.ContentType)
	if err != nil {
		return Started{}, err
	}
	intent := Intent{ID: intentID, OwnerID: ownerID, FileID: fileID, FolderID: input.FolderID, ObjectKey: key, StorageUploadID: storageID, IdempotencyKey: input.IdempotencyKey, OriginalName: input.Name, ContentType: input.ContentType, ChecksumSHA256: input.ChecksumSHA256, ExpectedSize: input.Size, PartSize: DefaultPartSize, Status: "PENDING", ExpiresAt: time.Now().UTC().Add(24 * time.Hour), CreatedAt: time.Now().UTC()}
	saved, created, err := s.repo.Create(ctx, intent)
	if err != nil {
		_ = s.store.AbortMultipart(context.WithoutCancel(ctx), key, storageID)
		return Started{}, err
	}
	if !created {
		_ = s.store.AbortMultipart(context.WithoutCancel(ctx), key, storageID)
		intent = saved
	}
	count := int(math.Ceil(float64(intent.ExpectedSize) / float64(intent.PartSize)))
	parts := make([]PartURL, 0, count)
	for number := 1; number <= count; number++ {
		url, err := s.store.PresignUploadPart(ctx, intent.ObjectKey, intent.StorageUploadID, int32(number), 15*time.Minute)
		if err != nil {
			return Started{}, err
		}
		parts = append(parts, PartURL{int32(number), url})
	}
	return Started{IntentID: intent.ID, FileID: intent.FileID, PartSize: intent.PartSize, ExpiresAt: intent.ExpiresAt, Parts: parts}, nil
}

func (s *Service) Resume(ctx context.Context, ownerID, intentID uuid.UUID) (Started, error) {
	intent, err := s.repo.Get(ctx, ownerID, intentID)
	if err != nil {
		return Started{}, err
	}
	if intent.Status != "PENDING" || time.Now().UTC().After(intent.ExpiresAt) {
		return Started{}, ErrUploadConflict
	}
	count := int(math.Ceil(float64(intent.ExpectedSize) / float64(intent.PartSize)))
	parts := make([]PartURL, 0, count)
	for number := 1; number <= count; number++ {
		url, err := s.store.PresignUploadPart(ctx, intent.ObjectKey, intent.StorageUploadID, int32(number), 15*time.Minute)
		if err != nil {
			return Started{}, err
		}
		parts = append(parts, PartURL{int32(number), url})
	}
	completed, err := s.store.ListParts(ctx, intent.ObjectKey, intent.StorageUploadID)
	if err != nil {
		return Started{}, err
	}
	return Started{IntentID: intent.ID, FileID: intent.FileID, PartSize: intent.PartSize, ExpiresAt: intent.ExpiresAt, Parts: parts, Completed: completed}, nil
}
func (s *Service) Complete(ctx context.Context, ownerID, intentID uuid.UUID, parts []storage.CompletedPart) (files.File, error) {
	if len(parts) == 0 {
		return files.File{}, ErrInvalidUpload
	}
	intent, err := s.repo.Get(ctx, ownerID, intentID)
	if err != nil {
		return files.File{}, err
	}
	if intent.Status != "PENDING" || time.Now().UTC().After(intent.ExpiresAt) {
		return files.File{}, ErrUploadConflict
	}
	expectedParts := int(math.Ceil(float64(intent.ExpectedSize) / float64(intent.PartSize)))
	if len(parts) != expectedParts {
		return files.File{}, ErrInvalidUpload
	}
	for index, part := range parts {
		if part.PartNumber != int32(index+1) || strings.TrimSpace(part.ETag) == "" {
			return files.File{}, ErrInvalidUpload
		}
	}
	if err = s.store.CompleteMultipart(ctx, intent.ObjectKey, intent.StorageUploadID, parts); err != nil {
		return files.File{}, err
	}
	fail := func(cause error) (files.File, error) {
		cleanupErr := s.store.Delete(context.WithoutCancel(ctx), intent.ObjectKey)
		stateErr := s.repo.Fail(context.WithoutCancel(ctx), ownerID, intentID)
		return files.File{}, errors.Join(cause, cleanupErr, stateErr)
	}
	if s.scanner != nil {
		body, getErr := s.store.Get(ctx, intent.ObjectKey)
		if getErr != nil {
			return fail(getErr)
		}
		scanErr := s.scanner.Scan(ctx, body)
		_ = body.Close()
		if scanErr != nil {
			return fail(scanErr)
		}
	}
	actual, err := s.store.ObjectSize(ctx, intent.ObjectKey)
	if err != nil {
		return fail(err)
	}
	if actual != intent.ExpectedSize {
		return fail(ErrSizeMismatch)
	}
	file, err := s.repo.Complete(ctx, ownerID, intentID, actual)
	if err != nil {
		return fail(err)
	}
	return file, err
}
func (s *Service) Cancel(ctx context.Context, ownerID, intentID uuid.UUID) error {
	intent, err := s.repo.Cancel(ctx, ownerID, intentID)
	if err != nil {
		return err
	}
	return s.store.AbortMultipart(ctx, intent.ObjectKey, intent.StorageUploadID)
}

func (s *Service) CleanupExpired(ctx context.Context, limit int) (int, error) {
	intents, err := s.repo.ExpireBatch(ctx, limit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	var cleanupErr error
	for _, intent := range intents {
		if err := s.store.AbortMultipart(ctx, intent.ObjectKey, intent.StorageUploadID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err, s.repo.MarkCleanupFailed(context.WithoutCancel(ctx), intent.ID))
			continue
		}
		if err := s.repo.MarkCleanupComplete(ctx, intent.ID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		cleaned++
	}
	return cleaned, cleanupErr
}

func (r *Repository) Create(ctx context.Context, intent Intent) (Intent, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Intent{}, false, err
	}
	defer tx.Rollback(ctx)
	existing, err := getByIdempotency(ctx, tx, intent.OwnerID, intent.IdempotencyKey)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, false, err
	}
	var used, reserved, quota int64
	if err = tx.QueryRow(ctx, `SELECT used_bytes,reserved_bytes,quota_bytes FROM users WHERE id=$1 FOR UPDATE`, intent.OwnerID).Scan(&used, &reserved, &quota); err != nil {
		return Intent{}, false, err
	}
	if used+reserved+intent.ExpectedSize > quota {
		return Intent{}, false, files.ErrQuotaExceeded
	}
	if intent.FolderID != nil {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM active_folders WHERE id=$1 AND owner_id=$2)`, *intent.FolderID, intent.OwnerID).Scan(&active); err != nil || !active {
			if err == nil {
				err = pgx.ErrNoRows
			}
			return Intent{}, false, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO upload_intents(id,owner_id,folder_id,file_id,object_key,storage_upload_id,idempotency_key,original_name,content_type,expected_size,checksum_sha256,part_size,status,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING',$13,$14)`, intent.ID, intent.OwnerID, intent.FolderID, intent.FileID, intent.ObjectKey, intent.StorageUploadID, intent.IdempotencyKey, intent.OriginalName, intent.ContentType, intent.ExpectedSize, intent.ChecksumSHA256, intent.PartSize, intent.ExpiresAt, intent.CreatedAt)
	if err != nil {
		return Intent{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes+$2 WHERE id=$1`, intent.OwnerID, intent.ExpectedSize); err != nil {
		return Intent{}, false, err
	}
	return intent, true, tx.Commit(ctx)
}
func getByIdempotency(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, key string) (Intent, error) {
	var i Intent
	err := tx.QueryRow(ctx, `SELECT id,owner_id,folder_id,file_id,object_key,storage_upload_id,idempotency_key,original_name,content_type,expected_size,checksum_sha256,part_size,status,expires_at,created_at FROM upload_intents WHERE owner_id=$1 AND idempotency_key=$2 AND status IN ('PENDING','COMPLETED')`, ownerID, key).Scan(&i.ID, &i.OwnerID, &i.FolderID, &i.FileID, &i.ObjectKey, &i.StorageUploadID, &i.IdempotencyKey, &i.OriginalName, &i.ContentType, &i.ExpectedSize, &i.ChecksumSHA256, &i.PartSize, &i.Status, &i.ExpiresAt, &i.CreatedAt)
	return i, err
}
func (r *Repository) Get(ctx context.Context, ownerID, id uuid.UUID) (Intent, error) {
	var i Intent
	err := r.db.QueryRow(ctx, `SELECT id,owner_id,folder_id,file_id,object_key,storage_upload_id,idempotency_key,original_name,content_type,expected_size,checksum_sha256,part_size,status,expires_at,created_at FROM upload_intents WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&i.ID, &i.OwnerID, &i.FolderID, &i.FileID, &i.ObjectKey, &i.StorageUploadID, &i.IdempotencyKey, &i.OriginalName, &i.ContentType, &i.ExpectedSize, &i.ChecksumSHA256, &i.PartSize, &i.Status, &i.ExpiresAt, &i.CreatedAt)
	return i, err
}
func (r *Repository) Complete(ctx context.Context, ownerID, id uuid.UUID, actual int64) (files.File, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return files.File{}, err
	}
	defer tx.Rollback(ctx)
	var i Intent
	err = tx.QueryRow(ctx, `SELECT id,owner_id,folder_id,file_id,object_key,storage_upload_id,idempotency_key,original_name,content_type,expected_size,checksum_sha256,part_size,status,expires_at,created_at FROM upload_intents WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&i.ID, &i.OwnerID, &i.FolderID, &i.FileID, &i.ObjectKey, &i.StorageUploadID, &i.IdempotencyKey, &i.OriginalName, &i.ContentType, &i.ExpectedSize, &i.ChecksumSHA256, &i.PartSize, &i.Status, &i.ExpiresAt, &i.CreatedAt)
	if err != nil {
		return files.File{}, err
	}
	if i.Status != "PENDING" || time.Now().UTC().After(i.ExpiresAt) {
		return files.File{}, ErrUploadConflict
	}
	if actual != i.ExpectedSize {
		return files.File{}, ErrSizeMismatch
	}
	file := files.File{ID: i.FileID, FolderID: i.FolderID, OriginalName: i.OriginalName, ContentType: i.ContentType, SizeBytes: actual, CreatedAt: time.Now().UTC(), ChecksumSHA256: i.ChecksumSHA256}
	_, err = tx.Exec(ctx, `INSERT INTO files(id,owner_id,folder_id,object_key,original_name,content_type,size_bytes,created_at,checksum_sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, file.ID, ownerID, file.FolderID, i.ObjectKey, file.OriginalName, file.ContentType, file.SizeBytes, file.CreatedAt, file.ChecksumSHA256)
	if err != nil {
		return files.File{}, err
	}
	versionID := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO file_versions(id,file_id,owner_id,object_key,content_type,size_bytes,checksum_sha256,created_at,thumbnail_status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $5 IN ('image/jpeg','image/png','image/webp','application/pdf') THEN 'PENDING' ELSE 'UNSUPPORTED' END)`, versionID, file.ID, ownerID, i.ObjectKey, file.ContentType, file.SizeBytes, file.ChecksumSHA256, file.CreatedAt); err != nil {
		return files.File{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE files SET current_version_id=$2 WHERE id=$1`, file.ID, versionID); err != nil {
		return files.File{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$2 WHERE id=$1 AND reserved_bytes >= $2`, ownerID, actual)
	if err != nil || tag.RowsAffected() == 0 {
		return files.File{}, fmt.Errorf("finalize quota: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE upload_intents SET status='COMPLETED',completed_at=NOW() WHERE id=$1`, id); err != nil {
		return files.File{}, err
	}
	return file, tx.Commit(ctx)
}

func (r *Repository) Fail(ctx context.Context, ownerID, id uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var expectedSize int64
	err = tx.QueryRow(ctx, `SELECT expected_size FROM upload_intents WHERE id=$1 AND owner_id=$2 AND status='PENDING' FOR UPDATE`, id, ownerID).Scan(&expectedSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE upload_intents SET status='FAILED',completed_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET reserved_bytes=GREATEST(0,reserved_bytes-$2) WHERE id=$1`, ownerID, expectedSize); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Cancel(ctx context.Context, ownerID, id uuid.UUID) (Intent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Intent{}, err
	}
	defer tx.Rollback(ctx)
	var i Intent
	err = tx.QueryRow(ctx, `SELECT id,object_key,storage_upload_id,expected_size,status FROM upload_intents WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&i.ID, &i.ObjectKey, &i.StorageUploadID, &i.ExpectedSize, &i.Status)
	if err != nil {
		return Intent{}, err
	}
	if i.Status != "PENDING" {
		return Intent{}, ErrUploadConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE upload_intents SET status='CANCELLED' WHERE id=$1`, id); err != nil {
		return Intent{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET reserved_bytes=GREATEST(0,reserved_bytes-$2) WHERE id=$1`, ownerID, i.ExpectedSize); err != nil {
		return Intent{}, err
	}
	return i, tx.Commit(ctx)
}

func (r *Repository) ExpireBatch(ctx context.Context, limit int) ([]Intent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,owner_id,object_key,storage_upload_id,expected_size,status FROM upload_intents
		WHERE (status='PENDING' AND expires_at<NOW())
		   OR (status='EXPIRED' AND storage_cleanup_pending=TRUE AND (storage_cleanup_claimed_at IS NULL OR storage_cleanup_claimed_at<NOW()-INTERVAL '15 minutes'))
		ORDER BY expires_at FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	intents := []Intent{}
	for rows.Next() {
		var i Intent
		if err := rows.Scan(&i.ID, &i.OwnerID, &i.ObjectKey, &i.StorageUploadID, &i.ExpectedSize, &i.Status); err != nil {
			rows.Close()
			return nil, err
		}
		intents = append(intents, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range intents {
		if _, err = tx.Exec(ctx, `UPDATE upload_intents SET status='EXPIRED',storage_cleanup_pending=TRUE,storage_cleanup_claimed_at=NOW() WHERE id=$1`, i.ID); err != nil {
			return nil, err
		}
		if i.Status == "PENDING" {
			if _, err = tx.Exec(ctx, `UPDATE users SET reserved_bytes=GREATEST(0,reserved_bytes-$2) WHERE id=$1`, i.OwnerID, i.ExpectedSize); err != nil {
				return nil, err
			}
		}
	}
	return intents, tx.Commit(ctx)
}

func (r *Repository) MarkCleanupComplete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE upload_intents SET storage_cleanup_pending=FALSE,storage_cleanup_claimed_at=NULL,storage_upload_id='' WHERE id=$1 AND status='EXPIRED'`, id)
	return err
}

func (r *Repository) MarkCleanupFailed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE upload_intents SET storage_cleanup_claimed_at=NULL,storage_cleanup_attempts=storage_cleanup_attempts+1 WHERE id=$1 AND status='EXPIRED' AND storage_cleanup_pending=TRUE`, id)
	return err
}
