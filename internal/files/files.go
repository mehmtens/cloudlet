package files

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mehmtens/cloudlet/internal/scanning"
	"golang.org/x/text/unicode/norm"
)

var ErrInvalidName = errors.New("file name must be between 1 and 255 characters")
var ErrInvalidSize = errors.New("file size must be zero or greater")
var ErrQuotaExceeded = errors.New("storage quota exceeded")
var ErrNameConflict = errors.New("a file with this name already exists")
var ErrDisallowedType = errors.New("this file type is not allowed")
var ErrInvalidBatch = errors.New("batch must contain between 1 and 100 unique file ids")

type File struct {
	ID             uuid.UUID  `json:"id"`
	FolderID       *uuid.UUID `json:"folder_id"`
	OriginalName   string     `json:"name"`
	ContentType    string     `json:"content_type"`
	SizeBytes      int64      `json:"size_bytes"`
	CreatedAt      time.Time  `json:"created_at"`
	ChecksumSHA256 string     `json:"checksum_sha256,omitempty"`
}

type Upload struct {
	Name, ContentType string
	Size              int64
	Body              io.Reader
	FolderID          *uuid.UUID
}

type StoredFile struct {
	File
	ObjectKey string
}

type TrashedFile struct {
	File
	DeletedAt time.Time `json:"deleted_at"`
}

type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type StorageUsage struct {
	UsedBytes      int64 `json:"used_bytes"`
	ReservedBytes  int64 `json:"reserved_bytes"`
	QuotaBytes     int64 `json:"quota_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}
type ListOptions struct {
	FolderID         *uuid.UUID
	Limit, Offset    int
	Sort, Order      string
	ContentType      *string
	MinSize, MaxSize *int64
}
type ObjectStore interface {
	Put(context.Context, string, string, io.Reader, int64) error
	Delete(context.Context, string) error
	PresignGet(context.Context, string, time.Duration) (string, error)
}
type Repository interface {
	Reserve(context.Context, uuid.UUID, int64) error
	Release(context.Context, uuid.UUID, int64) error
	Usage(context.Context, uuid.UUID) (StorageUsage, error)
	Create(context.Context, uuid.UUID, File, string) error
	List(context.Context, uuid.UUID, *uuid.UUID, int, int) ([]File, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (StoredFile, error)
	Rename(context.Context, uuid.UUID, uuid.UUID, string) (File, error)
	Move(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (File, error)
	BulkMove(context.Context, uuid.UUID, []uuid.UUID, *uuid.UUID) ([]File, error)
	BulkSoftDelete(context.Context, uuid.UUID, []uuid.UUID) error
	SoftDelete(context.Context, uuid.UUID, uuid.UUID) error
	ListTrash(context.Context, uuid.UUID, int, int) ([]TrashedFile, error)
	GetTrashed(context.Context, uuid.UUID, uuid.UUID) (StoredFile, error)
	Restore(context.Context, uuid.UUID, uuid.UUID) (File, error)
	PermanentlyDelete(context.Context, uuid.UUID, uuid.UUID) error
	TrashedVersionObjects(context.Context, uuid.UUID, uuid.UUID) ([]string, error)
	Search(context.Context, uuid.UUID, string, int) ([]File, error)
	ListAdvanced(context.Context, uuid.UUID, ListOptions) ([]File, error)
}

func (s *Service) Usage(ctx context.Context, ownerID uuid.UUID) (StorageUsage, error) {
	return s.repo.Usage(ctx, ownerID)
}

func (s *Service) Rename(ctx context.Context, ownerID, id uuid.UUID, name string) (File, error) {
	name = norm.NFC.String(strings.TrimSpace(name))
	if name == "" || len([]byte(name)) > 255 || filepath.Base(name) != name {
		return File{}, ErrInvalidName
	}
	return s.repo.Rename(ctx, ownerID, id, name)
}

func (s *Service) Move(ctx context.Context, ownerID, id uuid.UUID, folderID *uuid.UUID) (File, error) {
	return s.repo.Move(ctx, ownerID, id, folderID)
}

func (s *Service) BulkMove(ctx context.Context, ownerID uuid.UUID, ids []uuid.UUID, folderID *uuid.UUID) ([]File, error) {
	ids, ok := validBatch(ids)
	if !ok {
		return nil, ErrInvalidBatch
	}
	return s.repo.BulkMove(ctx, ownerID, ids, folderID)
}

func (s *Service) BulkDelete(ctx context.Context, ownerID uuid.UUID, ids []uuid.UUID) error {
	ids, ok := validBatch(ids)
	if !ok {
		return ErrInvalidBatch
	}
	return s.repo.BulkSoftDelete(ctx, ownerID, ids)
}

func validBatch(ids []uuid.UUID) ([]uuid.UUID, bool) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, false
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, false
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	return unique, true
}

func (s *Service) Delete(ctx context.Context, ownerID, id uuid.UUID) error {
	return s.repo.SoftDelete(ctx, ownerID, id)
}

func (s *Service) ListTrash(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]TrashedFile, error) {
	return s.repo.ListTrash(ctx, ownerID, limit, offset)
}

func (s *Service) Restore(ctx context.Context, ownerID, id uuid.UUID) (File, error) {
	return s.repo.Restore(ctx, ownerID, id)
}

func (s *Service) PermanentlyDelete(ctx context.Context, ownerID, id uuid.UUID) error {
	keys, err := s.repo.TrashedVersionObjects(ctx, ownerID, id)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.store.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete object: %w", err)
		}
	}
	if err := s.repo.PermanentlyDelete(ctx, ownerID, id); err != nil {
		return fmt.Errorf("delete file metadata: %w", err)
	}
	return nil
}

type Service struct {
	store   ObjectStore
	repo    Repository
	scanner scanning.Scanner
}

func (s *Service) List(ctx context.Context, ownerID uuid.UUID, folderID *uuid.UUID, limit, offset int) ([]File, error) {
	return s.repo.List(ctx, ownerID, folderID, limit, offset)
}
func (s *Service) ListAdvanced(ctx context.Context, ownerID uuid.UUID, options ListOptions) ([]File, error) {
	return s.repo.ListAdvanced(ctx, ownerID, options)
}

func (s *Service) Search(ctx context.Context, ownerID uuid.UUID, query string, limit int) ([]File, error) {
	query = norm.NFC.String(strings.TrimSpace(query))
	if query == "" {
		return []File{}, nil
	}
	return s.repo.Search(ctx, ownerID, query, limit)
}

func (s *Service) Get(ctx context.Context, ownerID, id uuid.UUID) (File, error) {
	stored, err := s.repo.Get(ctx, ownerID, id)
	return stored.File, err
}

func (s *Service) Download(ctx context.Context, ownerID, id uuid.UUID, lifetime time.Duration) (Download, error) {
	stored, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return Download{}, err
	}
	url, err := s.store.PresignGet(ctx, stored.ObjectKey, lifetime)
	if err != nil {
		return Download{}, err
	}
	return Download{URL: url, ExpiresAt: time.Now().UTC().Add(lifetime)}, nil
}

func NewService(store ObjectStore, repo Repository, scanners ...scanning.Scanner) *Service {
	var scanner scanning.Scanner
	if len(scanners) > 0 {
		scanner = scanners[0]
	}
	return &Service{store: store, repo: repo, scanner: scanner}
}

func (s *Service) Upload(ctx context.Context, ownerID uuid.UUID, upload Upload) (File, error) {
	if upload.Size < 0 {
		return File{}, ErrInvalidSize
	}
	if err := s.repo.Reserve(ctx, ownerID, upload.Size); err != nil {
		return File{}, err
	}
	reserved := true
	defer func() {
		if reserved {
			_ = s.repo.Release(context.WithoutCancel(ctx), ownerID, upload.Size)
		}
	}()
	if s.scanner != nil {
		spool, err := os.CreateTemp("", "cloudlet-scan-*")
		if err != nil {
			return File{}, err
		}
		defer os.Remove(spool.Name())
		defer spool.Close()
		if _, err = io.Copy(spool, io.LimitReader(upload.Body, upload.Size+1)); err != nil {
			return File{}, err
		}
		if _, err = spool.Seek(0, io.SeekStart); err != nil {
			return File{}, err
		}
		if err = s.scanner.Scan(ctx, spool); err != nil {
			return File{}, err
		}
		if _, err = spool.Seek(0, io.SeekStart); err != nil {
			return File{}, err
		}
		upload.Body = spool
	}
	id := uuid.New()
	name := norm.NFC.String(filepath.Base(strings.TrimSpace(upload.Name)))
	if name == "." || name == "" {
		name = "unnamed"
	}
	objectKey := "users/" + ownerID.String() + "/files/" + id.String() + "/versions/original"
	reader := bufio.NewReader(upload.Body)
	sample, _ := reader.Peek(512)
	detectedType := "application/octet-stream"
	if len(sample) > 0 {
		detectedType = http.DetectContentType(sample)
	}
	extension := strings.ToLower(filepath.Ext(name))
	if err := ValidateContentType(upload.ContentType, detectedType); err != nil || strings.HasPrefix(detectedType, "text/html") || extension == ".html" || extension == ".htm" || extension == ".svg" {
		return File{}, ErrDisallowedType
	}
	contentType := detectedType
	if detectedType == "application/octet-stream" && upload.ContentType != "" {
		contentType = upload.ContentType
	}
	file := File{ID: id, FolderID: upload.FolderID, OriginalName: name, ContentType: contentType, SizeBytes: upload.Size, CreatedAt: time.Now().UTC()}
	digest := sha256.New()
	if err := s.store.Put(ctx, objectKey, contentType, io.TeeReader(reader, digest), upload.Size); err != nil {
		return File{}, err
	}
	file.ChecksumSHA256 = hex.EncodeToString(digest.Sum(nil))
	if err := s.repo.Create(ctx, ownerID, file, objectKey); err != nil {
		_ = s.store.Delete(ctx, objectKey)
		return File{}, fmt.Errorf("save file metadata: %w", err)
	}
	reserved = false
	return file, nil
}

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{db: db} }
func (r *PostgresRepository) Reserve(ctx context.Context, ownerID uuid.UUID, size int64) error {
	command, err := r.db.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes+$2
		WHERE id=$1 AND used_bytes+reserved_bytes+$2 <= quota_bytes`, ownerID, size)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrQuotaExceeded
	}
	return nil
}

func (r *PostgresRepository) Release(ctx context.Context, ownerID uuid.UUID, size int64) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET reserved_bytes=GREATEST(0, reserved_bytes-$2) WHERE id=$1`, ownerID, size)
	return err
}

func (r *PostgresRepository) Usage(ctx context.Context, ownerID uuid.UUID) (StorageUsage, error) {
	var usage StorageUsage
	err := r.db.QueryRow(ctx, `SELECT used_bytes, reserved_bytes, quota_bytes,
		GREATEST(0, quota_bytes-used_bytes-reserved_bytes) FROM users WHERE id=$1`, ownerID).
		Scan(&usage.UsedBytes, &usage.ReservedBytes, &usage.QuotaBytes, &usage.AvailableBytes)
	return usage, err
}

func (r *PostgresRepository) Create(ctx context.Context, ownerID uuid.UUID, file File, objectKey string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if file.FolderID == nil {
		_, err = tx.Exec(ctx, `INSERT INTO files (id, owner_id, folder_id, object_key, original_name, content_type, size_bytes, created_at, checksum_sha256)
			VALUES ($1, $2, NULL, $3, $4, $5, $6, $7, $8)`, file.ID, ownerID, objectKey, file.OriginalName, file.ContentType, file.SizeBytes, file.CreatedAt, file.ChecksumSHA256)
		if err != nil {
			return mapNameConflict(err)
		}
	} else {
		var command pgconn.CommandTag
		command, err = tx.Exec(ctx, `INSERT INTO files (id, owner_id, folder_id, object_key, original_name, content_type, size_bytes, created_at, checksum_sha256)
			SELECT $1, $2, id, $4, $5, $6, $7, $8, $9 FROM active_folders
			WHERE id=$3 AND owner_id=$2`, file.ID, ownerID, *file.FolderID, objectKey, file.OriginalName, file.ContentType, file.SizeBytes, file.CreatedAt, file.ChecksumSHA256)
		if err == nil && command.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if err != nil {
			return mapNameConflict(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO file_versions(id,file_id,owner_id,object_key,content_type,size_bytes,checksum_sha256,created_at,thumbnail_status) VALUES($1,$1,$2,$3,$4,$5,$6,$7,CASE WHEN $4 IN ('image/jpeg','image/png','image/webp','application/pdf') THEN 'PENDING' ELSE 'UNSUPPORTED' END) ON CONFLICT DO NOTHING`, file.ID, ownerID, objectKey, file.ContentType, file.SizeBytes, file.ChecksumSHA256, file.CreatedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE files SET current_version_id=$2 WHERE id=$1`, file.ID, file.ID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes-$2, used_bytes=used_bytes+$2
		WHERE id=$1 AND reserved_bytes >= $2`, ownerID, file.SizeBytes)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return errors.New("storage reservation was not found")
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) List(ctx context.Context, ownerID uuid.UUID, folderID *uuid.UUID, limit, offset int) ([]File, error) {
	rows, err := r.db.Query(ctx, `SELECT id, folder_id, original_name, content_type, size_bytes, created_at
		FROM active_files WHERE owner_id=$1 AND folder_id IS NOT DISTINCT FROM $2
		ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4`, ownerID, folderID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]File, 0)
	for rows.Next() {
		var file File
		if err := rows.Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListAdvanced(ctx context.Context, ownerID uuid.UUID, o ListOptions) ([]File, error) {
	rows, err := r.db.Query(ctx, `SELECT id,folder_id,original_name,content_type,size_bytes,created_at FROM active_files WHERE owner_id=$1 AND folder_id IS NOT DISTINCT FROM $2 AND ($3::text IS NULL OR content_type=$3) AND ($4::bigint IS NULL OR size_bytes >= $4) AND ($5::bigint IS NULL OR size_bytes <= $5) ORDER BY CASE WHEN $6='name' AND $7='asc' THEN LOWER(original_name) END ASC, CASE WHEN $6='name' AND $7='desc' THEN LOWER(original_name) END DESC, CASE WHEN $6='size' AND $7='asc' THEN size_bytes END ASC, CASE WHEN $6='size' AND $7='desc' THEN size_bytes END DESC, CASE WHEN $6='created_at' AND $7='asc' THEN created_at END ASC, CASE WHEN $6='created_at' AND $7='desc' THEN created_at END DESC, id DESC LIMIT $8 OFFSET $9`, ownerID, o.FolderID, o.ContentType, o.MinSize, o.MaxSize, o.Sort, o.Order, o.Limit, o.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.FolderID, &f.OriginalName, &f.ContentType, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Get(ctx context.Context, ownerID, id uuid.UUID) (StoredFile, error) {
	var file StoredFile
	err := r.db.QueryRow(ctx, `SELECT id, folder_id, original_name, content_type, size_bytes, created_at, object_key, COALESCE(checksum_sha256,'')
		FROM active_files WHERE id = $1 AND owner_id = $2`, id, ownerID).Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt, &file.ObjectKey, &file.ChecksumSHA256)
	return file, mapNameConflict(err)
}

func (r *PostgresRepository) Search(ctx context.Context, ownerID uuid.UUID, query string, limit int) ([]File, error) {
	rows, err := r.db.Query(ctx, `SELECT id,folder_id,original_name,content_type,size_bytes,created_at FROM active_files WHERE owner_id=$1 AND original_name ILIKE '%' || $2 || '%' ORDER BY similarity(original_name,$2) DESC,created_at DESC LIMIT $3`, ownerID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []File{}
	for rows.Next() {
		var file File
		if err := rows.Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func mapNameConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameConflict
	}
	return err
}

func (r *PostgresRepository) Rename(ctx context.Context, ownerID, id uuid.UUID, name string) (File, error) {
	var file File
	err := r.db.QueryRow(ctx, `UPDATE files SET original_name = $3
		WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
		RETURNING id, folder_id, original_name, content_type, size_bytes, created_at`, id, ownerID, name).
		Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt)
	return file, mapNameConflict(err)
}

func (r *PostgresRepository) Move(ctx context.Context, ownerID, id uuid.UUID, folderID *uuid.UUID) (File, error) {
	var file File
	err := r.db.QueryRow(ctx, `UPDATE files SET folder_id=$3
		WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL
		AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM active_folders WHERE id=$3 AND owner_id=$2))
		RETURNING id,folder_id,original_name,content_type,size_bytes,created_at`, id, ownerID, folderID).
		Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt)
	return file, mapNameConflict(err)
}

func (r *PostgresRepository) BulkMove(ctx context.Context, ownerID uuid.UUID, ids []uuid.UUID, folderID *uuid.UUID) ([]File, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if folderID != nil {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM active_folders WHERE id=$1 AND owner_id=$2)`, *folderID, ownerID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, pgx.ErrNoRows
		}
	}
	rows, err := tx.Query(ctx, `UPDATE files SET folder_id=$3 WHERE owner_id=$1 AND id=ANY($2) AND deleted_at IS NULL
		RETURNING id,folder_id,original_name,content_type,size_bytes,created_at`, ownerID, ids, folderID)
	if err != nil {
		return nil, mapNameConflict(err)
	}
	defer rows.Close()
	result := make([]File, 0, len(ids))
	for rows.Next() {
		var file File
		if err = rows.Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, pgx.ErrNoRows
	}
	return result, tx.Commit(ctx)
}

func (r *PostgresRepository) BulkSoftDelete(ctx context.Context, ownerID uuid.UUID, ids []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE files SET deleted_at=NOW(),trash_batch_id=id
		WHERE owner_id=$1 AND id=ANY($2) AND deleted_at IS NULL`, ownerID, ids)
	if err != nil {
		return err
	}
	if command.RowsAffected() != int64(len(ids)) {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) SoftDelete(ctx context.Context, ownerID, id uuid.UUID) error {
	command, err := r.db.Exec(ctx, `UPDATE files SET deleted_at = NOW(), trash_batch_id = id
		WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL`, id, ownerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) ListTrash(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]TrashedFile, error) {
	rows, err := r.db.Query(ctx, `SELECT id, folder_id, original_name, content_type, size_bytes, created_at, deleted_at
		FROM files WHERE owner_id = $1 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC, id DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TrashedFile, 0)
	for rows.Next() {
		var file TrashedFile
		if err := rows.Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt, &file.DeletedAt); err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) GetTrashed(ctx context.Context, ownerID, id uuid.UUID) (StoredFile, error) {
	var file StoredFile
	err := r.db.QueryRow(ctx, `SELECT id, folder_id, original_name, content_type, size_bytes, created_at, object_key
		FROM files WHERE id = $1 AND owner_id = $2 AND deleted_at IS NOT NULL`, id, ownerID).
		Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt, &file.ObjectKey)
	return file, err
}

func (r *PostgresRepository) TrashedVersionObjects(ctx context.Context, ownerID, id uuid.UUID) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT v.object_key FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.id=$1 AND f.owner_id=$2 AND f.deleted_at IS NOT NULL UNION ALL SELECT v.thumbnail_object_key FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.id=$1 AND f.owner_id=$2 AND f.deleted_at IS NOT NULL AND v.thumbnail_object_key IS NOT NULL`, id, ownerID)
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
	if len(keys) == 0 {
		return nil, pgx.ErrNoRows
	}
	return keys, rows.Err()
}

func (r *PostgresRepository) Restore(ctx context.Context, ownerID, id uuid.UUID) (File, error) {
	var file File
	err := r.db.QueryRow(ctx, `UPDATE files SET deleted_at = NULL, trash_batch_id = NULL
		WHERE id = $1 AND owner_id = $2 AND deleted_at IS NOT NULL
		RETURNING id, folder_id, original_name, content_type, size_bytes, created_at`, id, ownerID).
		Scan(&file.ID, &file.FolderID, &file.OriginalName, &file.ContentType, &file.SizeBytes, &file.CreatedAt)
	return file, err
}

func (r *PostgresRepository) PermanentlyDelete(ctx context.Context, ownerID, id uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var size int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(v.size_bytes),0) FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.id=$1 AND f.owner_id=$2 AND f.deleted_at IS NOT NULL`, id, ownerID).Scan(&size); err != nil {
		return err
	}
	if tag, err := tx.Exec(ctx, `DELETE FROM files WHERE id=$1 AND owner_id=$2 AND deleted_at IS NOT NULL`, id, ownerID); err != nil || tag.RowsAffected() == 0 {
		if err == nil {
			return pgx.ErrNoRows
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET used_bytes=GREATEST(0, used_bytes-$2) WHERE id=$1`, ownerID, size); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
