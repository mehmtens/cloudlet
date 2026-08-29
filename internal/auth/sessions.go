package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const refreshTokenTTL = 30 * 24 * time.Hour

var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

type Tokens struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

type Session struct {
	ID         uuid.UUID  `json:"id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

type sessionRepository interface {
	CreateSession(context.Context, uuid.UUID, uuid.UUID, [32]byte, time.Time) error
	RotateSession(context.Context, [32]byte, [32]byte, uuid.UUID, time.Time, time.Time) (uuid.UUID, error)
	RevokeSession(context.Context, [32]byte, time.Time) error
	RevokeAllSessions(context.Context, uuid.UUID, time.Time) error
	RevokeSessionByID(context.Context, uuid.UUID, uuid.UUID, time.Time) error
	ListSessions(context.Context, uuid.UUID, time.Time) ([]Session, error)
}

func (r *Repository) CreateSession(ctx context.Context, id, userID uuid.UUID, hash [32]byte, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `INSERT INTO auth_sessions (id,user_id,refresh_token_hash,expires_at) VALUES ($1,$2,$3,$4)`, id, userID, hash[:], expiresAt)
	return err
}

func (r *Repository) RotateSession(ctx context.Context, oldHash, newHash [32]byte, newID uuid.UUID, expiresAt, now time.Time) (uuid.UUID, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM auth_sessions WHERE refresh_token_hash=$1 AND revoked_at IS NULL AND expires_at>$2 FOR UPDATE`, oldHash[:], now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidRefreshToken
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,last_used_at=$2 WHERE refresh_token_hash=$1`, oldHash[:], now); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions (id,user_id,refresh_token_hash,expires_at) VALUES ($1,$2,$3,$4)`, newID, userID, newHash[:], expiresAt); err != nil {
		return uuid.Nil, err
	}
	return userID, tx.Commit(ctx)
}

func (r *Repository) RevokeSession(ctx context.Context, hash [32]byte, now time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,last_used_at=$2 WHERE refresh_token_hash=$1 AND revoked_at IS NULL`, hash[:], now)
	return err
}
func (r *Repository) RevokeAllSessions(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
	return err
}
func (r *Repository) RevokeSessionByID(ctx context.Context, userID, id uuid.UUID, now time.Time) error {
	tag, err := r.db.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$3 WHERE user_id=$1 AND id=$2 AND revoked_at IS NULL`, userID, id, now)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (r *Repository) ListSessions(ctx context.Context, userID uuid.UUID, now time.Time) ([]Session, error) {
	rows, err := r.db.Query(ctx, `SELECT id,created_at,last_used_at,expires_at FROM auth_sessions WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>$2 ORDER BY created_at DESC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		var item Session
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.LastUsedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) RegisterWithSession(ctx context.Context, email, password string) (User, Tokens, error) {
	user, access, err := s.Register(ctx, email, password)
	if err != nil {
		return User{}, Tokens{}, err
	}
	tokens, err := s.createSession(ctx, user.ID, access)
	return user, tokens, err
}
func (s *Service) LoginWithSession(ctx context.Context, email, password string) (User, Tokens, error) {
	user, access, err := s.Login(ctx, email, password)
	if err != nil {
		return User{}, Tokens{}, err
	}
	tokens, err := s.createSession(ctx, user.ID, access)
	return user, tokens, err
}
func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	repo, ok := s.repo.(sessionRepository)
	if !ok || raw == "" {
		return Tokens{}, ErrInvalidRefreshToken
	}
	newRaw, newHash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	now := time.Now().UTC()
	refreshExpiry := now.Add(refreshTokenTTL)
	userID, err := repo.RotateSession(ctx, sha256.Sum256([]byte(raw)), newHash, uuid.New(), refreshExpiry, now)
	if err != nil {
		return Tokens{}, err
	}
	access, err := s.issueToken(User{ID: userID})
	return Tokens{access, newRaw, now.Add(s.tokenTTL), refreshExpiry}, err
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	repo, ok := s.repo.(sessionRepository)
	if !ok || raw == "" {
		return ErrInvalidRefreshToken
	}
	return repo.RevokeSession(ctx, sha256.Sum256([]byte(raw)), time.Now().UTC())
}
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	repo, ok := s.repo.(sessionRepository)
	if !ok {
		return ErrInvalidRefreshToken
	}
	return repo.RevokeAllSessions(ctx, userID, time.Now().UTC())
}
func (s *Service) Sessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	repo, ok := s.repo.(sessionRepository)
	if !ok {
		return nil, ErrInvalidRefreshToken
	}
	return repo.ListSessions(ctx, userID, time.Now().UTC())
}
func (s *Service) RevokeSessionByID(ctx context.Context, userID, id uuid.UUID) error {
	repo, ok := s.repo.(sessionRepository)
	if !ok {
		return ErrInvalidRefreshToken
	}
	return repo.RevokeSessionByID(ctx, userID, id, time.Now().UTC())
}
func (s *Service) createSession(ctx context.Context, userID uuid.UUID, access string) (Tokens, error) {
	repo, ok := s.repo.(sessionRepository)
	if !ok {
		return Tokens{}, ErrInvalidRefreshToken
	}
	raw, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	now := time.Now().UTC()
	expiry := now.Add(refreshTokenTTL)
	if err = repo.CreateSession(ctx, uuid.New(), userID, hash, expiry); err != nil {
		return Tokens{}, err
	}
	return Tokens{access, raw, now.Add(s.tokenTTL), expiry}, nil
}
func newRefreshToken() (string, [32]byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", [32]byte{}, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b[:])
	return raw, sha256.Sum256([]byte(raw)), nil
}
