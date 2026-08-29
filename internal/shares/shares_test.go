package shares

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type fakeRepository struct {
	created   Share
	tokenHash []byte
	stored    storedShare
	consumed  int
}

func (r *fakeRepository) Create(_ context.Context, _ uuid.UUID, share Share, hash []byte, _ *string) error {
	r.created = share
	r.tokenHash = append([]byte(nil), hash...)
	return nil
}
func (r *fakeRepository) List(context.Context, uuid.UUID, uuid.UUID) ([]Share, error) {
	return nil, nil
}
func (r *fakeRepository) Revoke(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeRepository) GetByTokenHash(context.Context, []byte) (storedShare, error) {
	return r.stored, nil
}
func (r *fakeRepository) Consume(context.Context, uuid.UUID) error { r.consumed++; return nil }

type fakeStore struct{}

func (fakeStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "https://storage.test/signed", nil
}

func TestCreateStoresHashAndReturnsRawURL(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(repo, fakeStore{}, "https://cloudlet.test")
	created, err := service.Create(context.Background(), uuid.New(), uuid.New(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.URL == "" {
		t.Fatal("expected one-time token and URL")
	}
	if string(repo.tokenHash) == created.Token {
		t.Fatal("raw token was stored instead of its hash")
	}
}

func TestPasswordProtectedDownload(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	repo := &fakeRepository{stored: storedShare{Share: Share{ID: uuid.New()}, PasswordHash: ptr(string(hash)), ObjectKey: "object"}}
	service := NewService(repo, fakeStore{}, "")
	if _, err := service.Download(context.Background(), "token", ""); err != ErrPasswordRequired {
		t.Fatalf("expected password required, got %v", err)
	}
	if _, err := service.Download(context.Background(), "token", "wrong"); err != ErrInvalidPassword {
		t.Fatalf("expected invalid password, got %v", err)
	}
	download, err := service.Download(context.Background(), "token", "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if download.URL == "" || repo.consumed != 1 {
		t.Fatal("expected one consumed download start")
	}
}

func TestExpiredShareIsUnavailable(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	repo := &fakeRepository{stored: storedShare{Share: Share{ID: uuid.New(), ExpiresAt: &past}}}
	_, err := NewService(repo, fakeStore{}, "").Download(context.Background(), "token", "")
	if err != ErrUnavailable {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

func ptr(value string) *string { return &value }
