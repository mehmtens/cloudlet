package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var ErrUnavailable = errors.New("share is unavailable")
var ErrPasswordRequired = errors.New("share password is required")
var ErrInvalidPassword = errors.New("share password is incorrect")
var ErrInvalidExpiry = errors.New("share expiry must be in the future")
var ErrInvalidDownloadLimit = errors.New("maximum downloads must be greater than zero")
var ErrEmailUnverified = errors.New("email verification is required to create public shares")
var ErrAccessExists = errors.New("file access already granted")

type Share struct {
	ID                uuid.UUID  `json:"id"`
	FileID            uuid.UUID  `json:"file_id"`
	ExpiresAt         *time.Time `json:"expires_at"`
	MaxAccessStarts   *int64     `json:"max_access_starts"`
	AccessStartCount  int64      `json:"access_start_count"`
	PasswordProtected bool       `json:"password_protected"`
	RevokedAt         *time.Time `json:"revoked_at"`
	CreatedAt         time.Time  `json:"created_at"`
}
type AccessGrant struct {
	ID           uuid.UUID `json:"id"`
	FileID       uuid.UUID `json:"file_id"`
	FileName     string    `json:"file_name"`
	OwnerEmail   string    `json:"owner_email"`
	GranteeEmail string    `json:"grantee_email,omitempty"`
	Permission   string    `json:"permission"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreatedShare struct {
	Share Share  `json:"share"`
	Token string `json:"-"`
	URL   string `json:"url"`
}
type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type storedShare struct {
	Share
	PasswordHash *string
	ObjectKey    string
}

type Repository interface {
	Create(context.Context, uuid.UUID, Share, []byte, *string) error
	List(context.Context, uuid.UUID, uuid.UUID) ([]Share, error)
	Revoke(context.Context, uuid.UUID, uuid.UUID) error
	GetByTokenHash(context.Context, []byte) (storedShare, error)
	Consume(context.Context, uuid.UUID) error
}
type ObjectStore interface {
	PresignGet(context.Context, string, time.Duration) (string, error)
}
type Service struct {
	repo          Repository
	store         ObjectStore
	publicBaseURL string
}

func NewService(repo Repository, store ObjectStore, publicBaseURL string) *Service {
	return &Service{repo: repo, store: store, publicBaseURL: publicBaseURL}
}

func (s *Service) Create(ctx context.Context, ownerID, fileID uuid.UUID, password string, expiresAt *time.Time, maxDownloads *int64) (CreatedShare, error) {
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return CreatedShare{}, ErrInvalidExpiry
	}
	if maxDownloads != nil && *maxDownloads <= 0 {
		return CreatedShare{}, ErrInvalidDownloadLimit
	}
	var passwordHash *string
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return CreatedShare{}, err
		}
		value := string(hash)
		passwordHash = &value
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return CreatedShare{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := sha256.Sum256([]byte(token))
	share := Share{ID: uuid.New(), FileID: fileID, ExpiresAt: expiresAt, MaxAccessStarts: maxDownloads, PasswordProtected: passwordHash != nil, CreatedAt: time.Now().UTC()}
	if err := s.repo.Create(ctx, ownerID, share, tokenHash[:], passwordHash); err != nil {
		return CreatedShare{}, err
	}
	return CreatedShare{Share: share, Token: token, URL: strings.TrimRight(s.publicBaseURL, "/") + "/s/" + token}, nil
}
func (s *Service) List(ctx context.Context, ownerID, fileID uuid.UUID) ([]Share, error) {
	return s.repo.List(ctx, ownerID, fileID)
}
func (s *Service) Revoke(ctx context.Context, ownerID, id uuid.UUID) error {
	return s.repo.Revoke(ctx, ownerID, id)
}

func (s *Service) Grant(ctx context.Context, ownerID, fileID uuid.UUID, email, permission string) (AccessGrant, error) {
	repo, ok := s.repo.(interface {
		Grant(context.Context, uuid.UUID, uuid.UUID, string, string) (AccessGrant, error)
	})
	if !ok {
		return AccessGrant{}, ErrUnavailable
	}
	return repo.Grant(ctx, ownerID, fileID, email, permission)
}

func (s *Service) UpdateGrant(ctx context.Context, ownerID, grantID uuid.UUID, permission string) error {
	repo, ok := s.repo.(interface {
		UpdateGrant(context.Context, uuid.UUID, uuid.UUID, string) error
	})
	if !ok {
		return ErrUnavailable
	}
	return repo.UpdateGrant(ctx, ownerID, grantID, permission)
}
func (s *Service) RevokeGrant(ctx context.Context, ownerID, grantID uuid.UUID) error {
	repo, ok := s.repo.(interface {
		RevokeGrant(context.Context, uuid.UUID, uuid.UUID) error
	})
	if !ok {
		return ErrUnavailable
	}
	return repo.RevokeGrant(ctx, ownerID, grantID)
}

func (s *Service) ListGranted(ctx context.Context, userID uuid.UUID) ([]AccessGrant, error) {
	repo, ok := s.repo.(interface {
		ListGranted(context.Context, uuid.UUID) ([]AccessGrant, error)
	})
	if !ok {
		return nil, ErrUnavailable
	}
	return repo.ListGranted(ctx, userID)
}

func (s *Service) ListOwnedGrants(ctx context.Context, ownerID, fileID uuid.UUID) ([]AccessGrant, error) {
	repo, ok := s.repo.(interface {
		ListOwnedGrants(context.Context, uuid.UUID, uuid.UUID) ([]AccessGrant, error)
	})
	if !ok {
		return nil, ErrUnavailable
	}
	return repo.ListOwnedGrants(ctx, ownerID, fileID)
}

func (s *Service) DownloadGranted(ctx context.Context, userID, fileID uuid.UUID) (Download, error) {
	repo, ok := s.repo.(interface {
		GrantedObjectKey(context.Context, uuid.UUID, uuid.UUID) (string, error)
	})
	if !ok {
		return Download{}, ErrUnavailable
	}
	key, err := repo.GrantedObjectKey(ctx, userID, fileID)
	if err != nil {
		return Download{}, err
	}
	url, err := s.store.PresignGet(ctx, key, 3*time.Minute)
	return Download{URL: url, ExpiresAt: time.Now().UTC().Add(3 * time.Minute)}, err
}

func (s *Service) Download(ctx context.Context, token, password string) (Download, error) {
	hash := sha256.Sum256([]byte(token))
	share, err := s.repo.GetByTokenHash(ctx, hash[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return Download{}, ErrUnavailable
	}
	if err != nil {
		return Download{}, err
	}
	if share.RevokedAt != nil || (share.ExpiresAt != nil && !share.ExpiresAt.After(time.Now())) || (share.MaxAccessStarts != nil && share.AccessStartCount >= *share.MaxAccessStarts) {
		return Download{}, ErrUnavailable
	}
	if share.PasswordHash != nil {
		if password == "" {
			return Download{}, ErrPasswordRequired
		}
		if bcrypt.CompareHashAndPassword([]byte(*share.PasswordHash), []byte(password)) != nil {
			return Download{}, ErrInvalidPassword
		}
	}
	if err := s.repo.Consume(ctx, share.ID); err != nil {
		return Download{}, ErrUnavailable
	}
	lifetime := 3 * time.Minute
	url, err := s.store.PresignGet(ctx, share.ObjectKey, lifetime)
	if err != nil {
		return Download{}, err
	}
	return Download{URL: url, ExpiresAt: time.Now().UTC().Add(lifetime)}, nil
}

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{db: db} }
func (r *PostgresRepository) Create(ctx context.Context, ownerID uuid.UUID, share Share, tokenHash []byte, passwordHash *string) error {
	var verified bool
	if err := r.db.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id=$1`, ownerID).Scan(&verified); err != nil {
		return err
	}
	if !verified {
		return ErrEmailUnverified
	}
	command, err := r.db.Exec(ctx, `INSERT INTO shares(id,file_id,owner_id,token_hash,password_hash,expires_at,max_access_starts,created_at)
		SELECT $1,f.id,$2,$4,$5,$6,$7,$8 FROM files f WHERE f.id=$3 AND f.owner_id=$2 AND f.deleted_at IS NULL`, share.ID, ownerID, share.FileID, tokenHash, passwordHash, share.ExpiresAt, share.MaxAccessStarts, share.CreatedAt)
	if err == nil && command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *PostgresRepository) Grant(ctx context.Context, ownerID, fileID uuid.UUID, email, permission string) (AccessGrant, error) {
	if permission != "read" && permission != "write" {
		return AccessGrant{}, errors.New("invalid permission")
	}
	var grant AccessGrant
	err := r.db.QueryRow(ctx, `INSERT INTO file_access_grants(id,file_id,owner_id,grantee_id,permission)
SELECT $1,f.id,f.owner_id,u.id,$4 FROM active_files f CROSS JOIN users u WHERE f.id=$2 AND f.owner_id=$3 AND LOWER(u.email)=LOWER($5) AND u.id<>$3
RETURNING id,file_id,permission,created_at`, uuid.New(), fileID, ownerID, permission, strings.TrimSpace(email)).Scan(&grant.ID, &grant.FileID, &grant.Permission, &grant.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessGrant{}, pgx.ErrNoRows
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return AccessGrant{}, ErrAccessExists
	}
	_ = r.db.QueryRow(ctx, `SELECT f.original_name,u.email FROM files f JOIN users u ON u.id=f.owner_id WHERE f.id=$1`, fileID).Scan(&grant.FileName, &grant.OwnerEmail)
	return grant, err
}
func (r *PostgresRepository) UpdateGrant(ctx context.Context, ownerID, grantID uuid.UUID, permission string) error {
	if permission != "read" && permission != "write" {
		return errors.New("invalid permission")
	}
	tag, err := r.db.Exec(ctx, `UPDATE file_access_grants SET permission=$3 WHERE id=$1 AND owner_id=$2`, grantID, ownerID, permission)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (r *PostgresRepository) RevokeGrant(ctx context.Context, ownerID, grantID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM file_access_grants WHERE id=$1 AND owner_id=$2`, grantID, ownerID)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (r *PostgresRepository) ListGranted(ctx context.Context, userID uuid.UUID) ([]AccessGrant, error) {
	rows, err := r.db.Query(ctx, `SELECT g.id,g.file_id,f.original_name,u.email,g.permission,g.created_at FROM file_access_grants g JOIN active_files f ON f.id=g.file_id JOIN users u ON u.id=g.owner_id WHERE g.grantee_id=$1 ORDER BY g.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AccessGrant{}
	for rows.Next() {
		var item AccessGrant
		if err := rows.Scan(&item.ID, &item.FileID, &item.FileName, &item.OwnerEmail, &item.Permission, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListOwnedGrants(ctx context.Context, ownerID, fileID uuid.UUID) ([]AccessGrant, error) {
	rows, err := r.db.Query(ctx, `SELECT g.id,g.file_id,f.original_name,u.email,g.permission,g.created_at FROM file_access_grants g JOIN active_files f ON f.id=g.file_id JOIN users u ON u.id=g.grantee_id WHERE g.owner_id=$1 AND g.file_id=$2 ORDER BY g.created_at DESC`, ownerID, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AccessGrant{}
	for rows.Next() {
		var item AccessGrant
		if err := rows.Scan(&item.ID, &item.FileID, &item.FileName, &item.GranteeEmail, &item.Permission, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) GrantedObjectKey(ctx context.Context, userID, fileID uuid.UUID) (string, error) {
	var key string
	err := r.db.QueryRow(ctx, `SELECT f.object_key FROM file_access_grants g JOIN active_files f ON f.id=g.file_id WHERE g.grantee_id=$1 AND g.file_id=$2`, userID, fileID).Scan(&key)
	return key, err
}
func (r *PostgresRepository) List(ctx context.Context, ownerID, fileID uuid.UUID) ([]Share, error) {
	rows, err := r.db.Query(ctx, `SELECT id,file_id,expires_at,max_access_starts,access_start_count,password_hash IS NOT NULL,revoked_at,created_at FROM shares WHERE owner_id=$1 AND file_id=$2 ORDER BY created_at DESC`, ownerID, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Share, 0)
	for rows.Next() {
		var item Share
		if err := rows.Scan(&item.ID, &item.FileID, &item.ExpiresAt, &item.MaxAccessStarts, &item.AccessStartCount, &item.PasswordProtected, &item.RevokedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (r *PostgresRepository) Revoke(ctx context.Context, ownerID, id uuid.UUID) error {
	command, err := r.db.Exec(ctx, `UPDATE shares SET revoked_at=COALESCE(revoked_at,NOW()) WHERE id=$1 AND owner_id=$2`, id, ownerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
func (r *PostgresRepository) GetByTokenHash(ctx context.Context, hash []byte) (storedShare, error) {
	var item storedShare
	err := r.db.QueryRow(ctx, `SELECT s.id,s.file_id,s.expires_at,s.max_access_starts,s.access_start_count,s.password_hash IS NOT NULL,s.revoked_at,s.created_at,s.password_hash,f.object_key FROM shares s JOIN files f ON f.id=s.file_id WHERE s.token_hash=$1 AND f.deleted_at IS NULL`, hash).Scan(&item.ID, &item.FileID, &item.ExpiresAt, &item.MaxAccessStarts, &item.AccessStartCount, &item.PasswordProtected, &item.RevokedAt, &item.CreatedAt, &item.PasswordHash, &item.ObjectKey)
	return item, err
}
func (r *PostgresRepository) Consume(ctx context.Context, id uuid.UUID) error {
	command, err := r.db.Exec(ctx, `UPDATE shares SET access_start_count=access_start_count+1 WHERE id=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>NOW()) AND (max_access_starts IS NULL OR access_start_count<max_access_starts)`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrUnavailable
	}
	return nil
}
