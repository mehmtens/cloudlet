package verification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidToken = errors.New("invalid or expired verification token")
var ErrAlreadyVerified = errors.New("email is already verified")
var ErrInvalidResetToken = errors.New("invalid or expired password reset token")
var ErrWeakPassword = errors.New("password must be at least 12 characters")

type Sender interface {
	SendVerification(context.Context, string, string) error
	SendPasswordReset(context.Context, string, string) error
}
type ShareNotifier interface {
	SendShareNotification(context.Context, string, string, string) error
}

type Service struct {
	db            *pgxpool.Pool
	sender        Sender
	publicBaseURL string
	tokenTTL      time.Duration
}

func NewService(db *pgxpool.Pool, sender Sender, publicBaseURL string) *Service {
	return &Service{db: db, sender: sender, publicBaseURL: strings.TrimRight(publicBaseURL, "/"), tokenTTL: 24 * time.Hour}
}

func (s *Service) NotifyShare(ctx context.Context, recipient, fileName, ownerEmail string) error {
	notifier, ok := s.sender.(ShareNotifier)
	if !ok {
		return nil
	}
	return notifier.SendShareNotification(ctx, recipient, fileName, ownerEmail)
}

func (s *Service) Issue(ctx context.Context, userID uuid.UUID, email string) error {
	var rawBytes [32]byte
	if _, err := rand.Read(rawBytes[:]); err != nil {
		return fmt.Errorf("create verification token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(rawBytes[:])
	hash := sha256.Sum256([]byte(raw))
	_, err := s.db.Exec(ctx, `INSERT INTO email_verification_tokens (user_id,token_hash,expires_at)
		VALUES ($1,$2,$3) ON CONFLICT (user_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,created_at=NOW()`, userID, hash[:], time.Now().UTC().Add(s.tokenTTL))
	if err != nil {
		return fmt.Errorf("store verification token: %w", err)
	}
	verificationURL := s.publicBaseURL + "/?verify=" + url.QueryEscape(raw)
	if err := s.sender.SendVerification(ctx, email, verificationURL); err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	return nil
}

func (s *Service) Resend(ctx context.Context, userID uuid.UUID) error {
	var email string
	var verified bool
	if err := s.db.QueryRow(ctx, `SELECT email,email_verified_at IS NOT NULL FROM users WHERE id=$1`, userID).Scan(&email, &verified); err != nil {
		return err
	}
	if verified {
		return ErrAlreadyVerified
	}
	return s.Issue(ctx, userID, email)
}

func (s *Service) Verify(ctx context.Context, raw string) error {
	if raw == "" {
		return ErrInvalidToken
	}
	hash := sha256.Sum256([]byte(raw))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM email_verification_tokens WHERE token_hash=$1 AND expires_at>NOW() FOR UPDATE`, hash[:]).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidToken
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET email_verified_at=COALESCE(email_verified_at,NOW()) WHERE id=$1`, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM email_verification_tokens WHERE user_id=$1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Status(ctx context.Context, userID uuid.UUID) (bool, error) {
	var verified bool
	err := s.db.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id=$1`, userID).Scan(&verified)
	return verified, err
}

func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	var userID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id FROM users WHERE email=$1`, email).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var rawBytes [32]byte
	if _, err := rand.Read(rawBytes[:]); err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(rawBytes[:])
	hash := sha256.Sum256([]byte(raw))
	_, err := s.db.Exec(ctx, `INSERT INTO password_reset_tokens (user_id,token_hash,expires_at)
		VALUES ($1,$2,$3) ON CONFLICT (user_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,created_at=NOW()`, userID, hash[:], time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		return fmt.Errorf("store password reset token: %w", err)
	}
	resetURL := s.publicBaseURL + "/?reset=" + url.QueryEscape(raw)
	if err := s.sender.SendPasswordReset(ctx, email, resetURL); err != nil {
		return fmt.Errorf("send password reset email: %w", err)
	}
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, raw, password string) error {
	if raw == "" {
		return ErrInvalidResetToken
	}
	if len(password) < 12 {
		return ErrWeakPassword
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tokenHash := sha256.Sum256([]byte(raw))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM password_reset_tokens WHERE token_hash=$1 AND expires_at>NOW() FOR UPDATE`, tokenHash[:]).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidResetToken
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, userID, string(passwordHash)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=NOW() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
