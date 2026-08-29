package versions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/scanning"
)

var ErrCurrentVersion = errors.New("cannot delete current version")

type Version struct {
	ID             uuid.UUID `json:"id"`
	FileID         uuid.UUID `json:"file_id"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	IsCurrent      bool      `json:"is_current"`
}
type Upload struct {
	Body        io.Reader
	Size        int64
	ContentType string
}
type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Store interface {
	Put(context.Context, string, string, io.Reader, int64) error
	Delete(context.Context, string) error
	PresignGet(context.Context, string, time.Duration) (string, error)
}
type Service struct {
	db      *pgxpool.Pool
	store   Store
	maxSize int64
	scanner scanning.Scanner
}

func NewService(db *pgxpool.Pool, store Store, maxSize int64, scanners ...scanning.Scanner) *Service {
	var scanner scanning.Scanner
	if len(scanners) > 0 {
		scanner = scanners[0]
	}
	return &Service{db: db, store: store, maxSize: maxSize, scanner: scanner}
}

func (s *Service) Upload(ctx context.Context, ownerID, fileID uuid.UUID, input Upload) (Version, error) {
	if input.Size < 0 || input.Size > s.maxSize {
		return Version{}, files.ErrInvalidSize
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM active_files WHERE id=$1 AND owner_id=$2)`, fileID, ownerID).Scan(&exists); err != nil {
		return Version{}, err
	}
	if !exists {
		return Version{}, pgx.ErrNoRows
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes+$2 WHERE id=$1 AND used_bytes+reserved_bytes+$2<=quota_bytes`, ownerID, input.Size)
	if err != nil {
		return Version{}, err
	}
	if tag.RowsAffected() == 0 {
		return Version{}, files.ErrQuotaExceeded
	}
	reserved := true
	defer func() {
		if reserved {
			_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE users SET reserved_bytes=GREATEST(0,reserved_bytes-$2) WHERE id=$1`, ownerID, input.Size)
		}
	}()
	if s.scanner != nil {
		spool, err := os.CreateTemp("", "cloudlet-version-scan-*")
		if err != nil {
			return Version{}, err
		}
		defer os.Remove(spool.Name())
		defer spool.Close()
		if _, err = io.Copy(spool, io.LimitReader(input.Body, input.Size+1)); err != nil {
			return Version{}, err
		}
		if _, err = spool.Seek(0, io.SeekStart); err != nil {
			return Version{}, err
		}
		if err = s.scanner.Scan(ctx, spool); err != nil {
			return Version{}, err
		}
		if _, err = spool.Seek(0, io.SeekStart); err != nil {
			return Version{}, err
		}
		input.Body = spool
	}
	reader := bufio.NewReader(input.Body)
	sample, _ := reader.Peek(512)
	contentType := "application/octet-stream"
	if len(sample) > 0 {
		contentType = http.DetectContentType(sample)
	}
	if err := files.ValidateContentType(input.ContentType, contentType); err != nil || strings.HasPrefix(contentType, "text/html") {
		return Version{}, files.ErrDisallowedType
	}
	versionID := uuid.New()
	key := "users/" + ownerID.String() + "/files/" + fileID.String() + "/versions/" + versionID.String()
	digest := sha256.New()
	if err := s.store.Put(ctx, key, contentType, io.TeeReader(reader, digest), input.Size); err != nil {
		return Version{}, err
	}
	checksum := hex.EncodeToString(digest.Sum(nil))
	version := Version{ID: versionID, FileID: fileID, ContentType: contentType, SizeBytes: input.Size, ChecksumSHA256: checksum, CreatedAt: time.Now().UTC(), IsCurrent: true}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		_ = s.store.Delete(ctx, key)
		return Version{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO file_versions(id,file_id,owner_id,object_key,content_type,size_bytes,checksum_sha256,created_at,thumbnail_status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $5 IN ('image/jpeg','image/png','image/webp','application/pdf') THEN 'PENDING' ELSE 'UNSUPPORTED' END)`, version.ID, fileID, ownerID, key, version.ContentType, version.SizeBytes, version.ChecksumSHA256, version.CreatedAt)
	if err != nil {
		_ = s.store.Delete(ctx, key)
		return Version{}, err
	}
	tag, err = tx.Exec(ctx, `UPDATE files SET current_version_id=$3,object_key=$4,content_type=$5,size_bytes=$6,checksum_sha256=$7 WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL`, fileID, ownerID, version.ID, key, version.ContentType, version.SizeBytes, version.ChecksumSHA256)
	if err != nil || tag.RowsAffected() == 0 {
		_ = s.store.Delete(ctx, key)
		if err == nil {
			err = pgx.ErrNoRows
		}
		return Version{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$2 WHERE id=$1 AND reserved_bytes >= $2`, ownerID, input.Size); err != nil {
		_ = s.store.Delete(ctx, key)
		return Version{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		_ = s.store.Delete(ctx, key)
		return Version{}, err
	}
	reserved = false
	return version, nil
}
func (s *Service) List(ctx context.Context, ownerID, fileID uuid.UUID) ([]Version, error) {
	rows, err := s.db.Query(ctx, `SELECT v.id,v.file_id,v.content_type,v.size_bytes,COALESCE(v.checksum_sha256,''),v.created_at,(v.id=f.current_version_id) FROM file_versions v JOIN active_files f ON f.id=v.file_id WHERE v.file_id=$1 AND v.owner_id=$2 ORDER BY v.created_at DESC`, fileID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.FileID, &v.ContentType, &v.SizeBytes, &v.ChecksumSHA256, &v.CreatedAt, &v.IsCurrent); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Service) Download(ctx context.Context, ownerID, fileID, versionID uuid.UUID) (Download, error) {
	var key string
	if err := s.db.QueryRow(ctx, `SELECT v.object_key FROM file_versions v JOIN active_files f ON f.id=v.file_id WHERE v.id=$1 AND v.file_id=$2 AND v.owner_id=$3`, versionID, fileID, ownerID).Scan(&key); err != nil {
		return Download{}, err
	}
	url, err := s.store.PresignGet(ctx, key, 3*time.Minute)
	return Download{url, time.Now().UTC().Add(3 * time.Minute)}, err
}
func (s *Service) Restore(ctx context.Context, ownerID, fileID, versionID uuid.UUID) (Version, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback(ctx)
	var v Version
	var key string
	err = tx.QueryRow(ctx, `SELECT id,file_id,content_type,size_bytes,COALESCE(checksum_sha256,''),created_at,object_key FROM file_versions WHERE id=$1 AND file_id=$2 AND owner_id=$3 FOR UPDATE`, versionID, fileID, ownerID).Scan(&v.ID, &v.FileID, &v.ContentType, &v.SizeBytes, &v.ChecksumSHA256, &v.CreatedAt, &key)
	if err != nil {
		return Version{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE files SET current_version_id=$3,object_key=$4,content_type=$5,size_bytes=$6,checksum_sha256=$7 WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL`, fileID, ownerID, v.ID, key, v.ContentType, v.SizeBytes, v.ChecksumSHA256)
	if err != nil || tag.RowsAffected() == 0 {
		if err == nil {
			err = pgx.ErrNoRows
		}
		return Version{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Version{}, err
	}
	v.IsCurrent = true
	return v, nil
}

func (s *Service) Delete(ctx context.Context, ownerID, fileID, versionID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var key, thumbnail string
	var size int64
	var current bool
	err = tx.QueryRow(ctx, `SELECT v.object_key,COALESCE(v.thumbnail_object_key,''),v.size_bytes,(v.id=f.current_version_id) FROM file_versions v JOIN files f ON f.id=v.file_id WHERE v.id=$1 AND v.file_id=$2 AND v.owner_id=$3 FOR UPDATE`, versionID, fileID, ownerID).Scan(&key, &thumbnail, &size, &current)
	if err != nil {
		return err
	}
	if current {
		return ErrCurrentVersion
	}
	if _, err = tx.Exec(ctx, `DELETE FROM file_versions WHERE id=$1`, versionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET used_bytes=GREATEST(0,used_bytes-$2) WHERE id=$1`, ownerID, size); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if err = s.store.Delete(ctx, key); err != nil {
		return err
	}
	if thumbnail != "" {
		_ = s.store.Delete(ctx, thumbnail)
	}
	return nil
}

type prunableVersion struct {
	ID                 uuid.UUID
	ObjectKey          string
	ThumbnailObjectKey string
	SizeBytes          int64
}

// PruneAll keeps the current version plus the newest non-current versions.
// Trashed files are deliberately excluded through active_files.
func (s *Service) PruneAll(ctx context.Context, maxVersions, batch int) (int, error) {
	if maxVersions < 1 || batch < 1 {
		return 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT f.owner_id,f.id FROM active_files f WHERE (SELECT COUNT(*) FROM file_versions v WHERE v.file_id=f.id)>$1 ORDER BY f.created_at LIMIT $2`, maxVersions, batch)
	if err != nil {
		return 0, err
	}
	type target struct{ ownerID, fileID uuid.UUID }
	targets := []target{}
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.ownerID, &t.fileID); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, t := range targets {
		n, err := s.pruneFile(ctx, t.ownerID, t.fileID, maxVersions)
		if err != nil {
			return pruned, err
		}
		pruned += n
	}
	return pruned, nil
}

func (s *Service) pruneFile(ctx context.Context, ownerID, fileID uuid.UUID, maxVersions int) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT v.id,v.object_key,COALESCE(v.thumbnail_object_key,''),v.size_bytes FROM file_versions v JOIN active_files f ON f.id=v.file_id WHERE v.file_id=$1 AND v.owner_id=$2 AND v.id<>f.current_version_id ORDER BY v.created_at DESC OFFSET $3 FOR UPDATE OF v`, fileID, ownerID, maxVersions-1)
	if err != nil {
		return 0, err
	}
	versions := []prunableVersion{}
	for rows.Next() {
		var v prunableVersion
		if err := rows.Scan(&v.ID, &v.ObjectKey, &v.ThumbnailObjectKey, &v.SizeBytes); err != nil {
			rows.Close()
			return 0, err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var released int64
	for _, v := range versions {
		if err := s.store.Delete(ctx, v.ObjectKey); err != nil {
			return 0, err
		}
		if v.ThumbnailObjectKey != "" {
			if err := s.store.Delete(ctx, v.ThumbnailObjectKey); err != nil {
				return 0, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM file_versions WHERE id=$1`, v.ID); err != nil {
			return 0, err
		}
		released += v.SizeBytes
	}
	if released > 0 {
		if _, err := tx.Exec(ctx, `UPDATE users SET used_bytes=GREATEST(0,used_bytes-$2) WHERE id=$1`, ownerID, released); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(versions), nil
}
