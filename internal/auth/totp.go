package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mehmtens/cloudlet/internal/totp"
)

var ErrTOTPNotConfigured = errors.New("TOTP is not configured")
var ErrInvalidTOTP = errors.New("invalid TOTP code")
var ErrTOTPRequired = errors.New("TOTP code required")
var ErrTOTPAlreadyEnabled = errors.New("TOTP is already enabled")

type totpRepository interface {
	TOTPByID(context.Context, uuid.UUID) (string, bool, error)
	SetTOTP(context.Context, uuid.UUID, string, *time.Time) error
}

func (r *Repository) TOTPByID(ctx context.Context, id uuid.UUID) (string, bool, error) {
	var secret string
	var enabledAt *time.Time
	err := r.db.QueryRow(ctx, `SELECT COALESCE(totp_secret,''), totp_enabled_at FROM users WHERE id=$1`, id).Scan(&secret, &enabledAt)
	return secret, enabledAt != nil, err
}

func (r *Repository) SetTOTP(ctx context.Context, id uuid.UUID, secret string, enabledAt *time.Time) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET totp_secret=$2, totp_enabled_at=$3 WHERE id=$1`, id, secret, enabledAt)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Service) TOTPStatus(ctx context.Context, id uuid.UUID) (bool, error) {
	repo, ok := s.repo.(totpRepository)
	if !ok {
		return false, errors.New("TOTP is unavailable")
	}
	_, enabled, err := repo.TOTPByID(ctx, id)
	return enabled, err
}

func (s *Service) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	repo, ok := s.repo.(passwordRepository)
	if !ok {
		return User{}, errors.New("user lookup is unavailable")
	}
	user, _, err := repo.CredentialsByID(ctx, id)
	return user, err
}

func (s *Service) BeginTOTP(ctx context.Context, id uuid.UUID) (string, error) {
	repo, ok := s.repo.(totpRepository)
	if !ok {
		return "", errors.New("TOTP is unavailable")
	}
	if _, enabled, err := repo.TOTPByID(ctx, id); err != nil {
		return "", err
	} else if enabled {
		return "", ErrTOTPAlreadyEnabled
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		return "", err
	}
	stored, err := s.encryptTOTPSecret(id, secret)
	if err != nil {
		return "", err
	}
	if err := repo.SetTOTP(ctx, id, stored, nil); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Service) EnableTOTP(ctx context.Context, id uuid.UUID, code string) error {
	repo, ok := s.repo.(totpRepository)
	if !ok {
		return errors.New("TOTP is unavailable")
	}
	stored, enabled, err := repo.TOTPByID(ctx, id)
	if err != nil {
		return err
	}
	if stored == "" {
		return ErrTOTPNotConfigured
	}
	secret, err := s.decryptTOTPSecret(id, stored)
	if err != nil {
		return err
	}
	if enabled || !totp.Validate(secret, code, time.Now().UTC()) {
		return ErrInvalidTOTP
	}
	now := time.Now().UTC()
	encrypted, err := s.encryptTOTPSecret(id, secret)
	if err != nil {
		return err
	}
	return repo.SetTOTP(ctx, id, encrypted, &now)
}

func (s *Service) DisableTOTP(ctx context.Context, id uuid.UUID, code string) error {
	repo, ok := s.repo.(totpRepository)
	if !ok {
		return errors.New("TOTP is unavailable")
	}
	stored, enabled, err := repo.TOTPByID(ctx, id)
	if err != nil {
		return err
	}
	secret, err := s.decryptTOTPSecret(id, stored)
	if err != nil {
		return err
	}
	if !enabled || !totp.Validate(secret, code, time.Now().UTC()) {
		return ErrInvalidTOTP
	}
	return repo.SetTOTP(ctx, id, "", nil)
}

func (s *Service) LoginWithSessionCode(ctx context.Context, email, password, code string) (User, Tokens, error) {
	user, access, err := s.Login(ctx, email, password)
	if err != nil {
		return User{}, Tokens{}, err
	}
	if repo, ok := s.repo.(totpRepository); ok {
		stored, enabled, lookupErr := repo.TOTPByID(ctx, user.ID)
		if lookupErr != nil {
			return User{}, Tokens{}, lookupErr
		}
		if enabled {
			secret, decryptErr := s.decryptTOTPSecret(user.ID, stored)
			if decryptErr != nil {
				return User{}, Tokens{}, decryptErr
			}
			if code == "" {
				return User{}, Tokens{}, ErrTOTPRequired
			}
			if !totp.Validate(secret, code, time.Now().UTC()) {
				return User{}, Tokens{}, ErrInvalidTOTP
			}
		}
	}
	tokens, err := s.createSession(ctx, user.ID, access)
	return user, tokens, err
}

const encryptedTOTPPrefix = "v1."

func (s *Service) encryptTOTPSecret(userID uuid.UUID, secret string) (string, error) {
	if len(s.totpKey) != 32 {
		return "", errors.New("TOTP encryption is not configured")
	}
	block, err := aes.NewCipher(s.totpKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(secret), userID[:])
	return encryptedTOTPPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Service) decryptTOTPSecret(userID uuid.UUID, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, encryptedTOTPPrefix) {
		// Backward compatibility for secrets written before encryption was added.
		return stored, nil
	}
	if len(s.totpKey) != 32 {
		return "", errors.New("TOTP encryption is not configured")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, encryptedTOTPPrefix))
	if err != nil {
		return "", fmt.Errorf("decode TOTP secret: %w", err)
	}
	block, err := aes.NewCipher(s.totpKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted TOTP secret")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, userID[:])
	if err != nil {
		return "", errors.New("decrypt TOTP secret")
	}
	return string(plaintext), nil
}
