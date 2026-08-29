package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memorySession struct {
	id, userID uuid.UUID
	hash       [32]byte
	expires    time.Time
	revoked    bool
}
type memoryAuthRepository struct {
	memoryUsers
	sessions map[[32]byte]memorySession
}

func (m *memoryAuthRepository) CreateSession(_ context.Context, id, userID uuid.UUID, hash [32]byte, expires time.Time) error {
	if m.sessions == nil {
		m.sessions = map[[32]byte]memorySession{}
	}
	m.sessions[hash] = memorySession{id: id, userID: userID, hash: hash, expires: expires}
	return nil
}
func (m *memoryAuthRepository) RotateSession(_ context.Context, oldHash, newHash [32]byte, newID uuid.UUID, expires, now time.Time) (uuid.UUID, error) {
	item, ok := m.sessions[oldHash]
	if !ok || item.revoked || !item.expires.After(now) {
		return uuid.Nil, ErrInvalidRefreshToken
	}
	item.revoked = true
	m.sessions[oldHash] = item
	m.sessions[newHash] = memorySession{id: newID, userID: item.userID, hash: newHash, expires: expires}
	return item.userID, nil
}
func (m *memoryAuthRepository) RevokeSession(_ context.Context, hash [32]byte, _ time.Time) error {
	item := m.sessions[hash]
	item.revoked = true
	m.sessions[hash] = item
	return nil
}
func (m *memoryAuthRepository) RevokeAllSessions(_ context.Context, userID uuid.UUID, _ time.Time) error {
	for key, item := range m.sessions {
		if item.userID == userID {
			item.revoked = true
			m.sessions[key] = item
		}
	}
	return nil
}
func (m *memoryAuthRepository) RevokeSessionByID(_ context.Context, userID, id uuid.UUID, _ time.Time) error {
	for key, item := range m.sessions {
		if item.userID == userID && item.id == id {
			item.revoked = true
			m.sessions[key] = item
			return nil
		}
	}
	return errors.New("not found")
}
func (m *memoryAuthRepository) ListSessions(_ context.Context, userID uuid.UUID, now time.Time) ([]Session, error) {
	result := []Session{}
	for _, item := range m.sessions {
		if item.userID == userID && !item.revoked && item.expires.After(now) {
			result = append(result, Session{ID: item.id, ExpiresAt: item.expires})
		}
	}
	return result, nil
}

type memoryUsers struct {
	user User
	hash string
}

func (m *memoryUsers) Create(_ context.Context, user User, hash string) error {
	m.user, m.hash = user, hash
	return nil
}
func (m *memoryUsers) CredentialsByEmail(_ context.Context, _ string) (User, string, error) {
	return m.user, m.hash, nil
}
func (m *memoryAuthRepository) CredentialsByID(_ context.Context, id uuid.UUID) (User, string, error) {
	if m.user.ID != id {
		return User{}, "", errors.New("not found")
	}
	return m.user, m.hash, nil
}
func (m *memoryAuthRepository) UpdatePassword(_ context.Context, id uuid.UUID, hash string) error {
	if m.user.ID != id {
		return errors.New("not found")
	}
	m.hash = hash
	return nil
}

func TestRegisterHashesPasswordAndIssuesToken(t *testing.T) {
	repo := &memoryUsers{}
	service := NewService(repo, "01234567890123456789012345678901", time.Hour)
	user, token, err := service.Register(context.Background(), "USER@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	if repo.hash == "password12345" {
		t.Fatal("password was stored in plaintext")
	}
	if user.Email != "user@example.com" {
		t.Fatalf("unexpected normalized email %q", user.Email)
	}
	ownerID, err := service.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if ownerID != user.ID {
		t.Fatalf("expected owner %s, got %s", user.ID, ownerID)
	}
}

func TestRegisterRejectsWeakPassword(t *testing.T) {
	service := NewService(&memoryUsers{}, "01234567890123456789012345678901", time.Hour)
	_, _, err := service.Register(context.Background(), "user@example.com", "short")
	if !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected weak password error, got %v", err)
	}
}

func TestParseTokenRejectsDifferentSecret(t *testing.T) {
	issuer := NewService(&memoryUsers{}, "01234567890123456789012345678901", time.Hour)
	_, token, err := issuer.Register(context.Background(), "user@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	validator := NewService(&memoryUsers{}, "abcdefghijklmnopqrstuvwxyz123456", time.Hour)
	ownerID, err := validator.ParseToken(token)
	if err == nil || ownerID != uuid.Nil {
		t.Fatal("expected token signed with another secret to be rejected")
	}
}

func TestRefreshTokenRotatesAndCannotBeReused(t *testing.T) {
	repo := &memoryAuthRepository{}
	service := NewService(repo, "01234567890123456789012345678901", 15*time.Minute)
	user, tokens, err := service.RegisterWithSession(context.Background(), "user@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Refresh(context.Background(), tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := service.ParseToken(rotated.AccessToken)
	if err != nil || ownerID != user.ID {
		t.Fatal("rotated access token is invalid")
	}
	if _, err = service.Refresh(context.Background(), tokens.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("expected reused refresh token to fail, got %v", err)
	}
}

func TestLogoutAllRevokesRefreshTokens(t *testing.T) {
	repo := &memoryAuthRepository{}
	service := NewService(repo, "01234567890123456789012345678901", 15*time.Minute)
	user, tokens, err := service.RegisterWithSession(context.Background(), "user@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.LogoutAll(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Refresh(context.Background(), tokens.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatal("expected revoked refresh token to fail")
	}
}

func TestChangePasswordVerifiesAndRevokesSessions(t *testing.T) {
	repo := &memoryAuthRepository{}
	service := NewService(repo, "01234567890123456789012345678901", 15*time.Minute)
	user, tokens, err := service.RegisterWithSession(context.Background(), "user@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ChangePassword(context.Background(), user.ID, "wrong-password", "newpassword123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected current password rejection, got %v", err)
	}
	if err = service.ChangePassword(context.Background(), user.ID, "password12345", "newpassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Refresh(context.Background(), tokens.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("expected old session to be revoked, got %v", err)
	}
	if _, _, err = service.LoginWithSession(context.Background(), user.Email, "password12345"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected old password to fail, got %v", err)
	}
	if _, _, err = service.LoginWithSession(context.Background(), user.Email, "newpassword123"); err != nil {
		t.Fatalf("expected new password to work, got %v", err)
	}
}
