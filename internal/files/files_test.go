package files

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	putKey     string
	deletedKey string
	presignURL string
}

func (s *fakeStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
	s.putKey = key
	_, _ = io.ReadAll(body)
	return nil
}
func (s *fakeStore) Delete(_ context.Context, key string) error { s.deletedKey = key; return nil }
func (s *fakeStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, error) {
	return s.presignURL, nil
}

type fakeRepository struct {
	createErr          error
	stored             StoredFile
	permanentlyDeleted bool
	reserved           int64
	created            File
}

func (r *fakeRepository) Reserve(_ context.Context, _ uuid.UUID, size int64) error {
	r.reserved += size
	return nil
}
func (r *fakeRepository) Release(_ context.Context, _ uuid.UUID, size int64) error {
	r.reserved -= size
	return nil
}
func (r *fakeRepository) Usage(context.Context, uuid.UUID) (StorageUsage, error) {
	return StorageUsage{}, nil
}

func (r *fakeRepository) Create(_ context.Context, _ uuid.UUID, file File, _ string) error {
	r.created = file
	if r.createErr == nil {
		r.reserved = 0
	}
	return r.createErr
}

func TestUploadCalculatesSHA256AndDetectsMIME(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(&fakeStore{}, repo)
	file, err := service.Upload(context.Background(), uuid.New(), Upload{Name: "note.txt", ContentType: "application/octet-stream", Size: 4, Body: strings.NewReader("data")})
	if err != nil {
		t.Fatal(err)
	}
	if file.ChecksumSHA256 != "3a6eb0790f39ac87c94f3856b2dd2c5d110e6811602261a9a923d3bb23adc8b7" {
		t.Fatalf("unexpected checksum %q", file.ChecksumSHA256)
	}
	if !strings.HasPrefix(file.ContentType, "text/plain") {
		t.Fatalf("expected detected text MIME, got %q", file.ContentType)
	}
}

func TestUploadRejectsHTMLContent(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(&fakeStore{}, repo)
	_, err := service.Upload(context.Background(), uuid.New(), Upload{Name: "page.html", ContentType: "text/html", Size: 13, Body: strings.NewReader("<html></html>")})
	if !errors.Is(err, ErrDisallowedType) {
		t.Fatalf("expected disallowed type, got %v", err)
	}
	if repo.reserved != 0 {
		t.Fatalf("expected reservation release, got %d", repo.reserved)
	}
}
func (r *fakeRepository) List(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _, _ int) ([]File, error) {
	return nil, nil
}
func (r *fakeRepository) Search(_ context.Context, _ uuid.UUID, _ string, _ int) ([]File, error) {
	return []File{}, nil
}
func (r *fakeRepository) ListAdvanced(_ context.Context, _ uuid.UUID, _ ListOptions) ([]File, error) {
	return []File{}, nil
}
func (r *fakeRepository) Get(_ context.Context, _, _ uuid.UUID) (StoredFile, error) {
	return r.stored, nil
}
func (r *fakeRepository) Rename(_ context.Context, _, _ uuid.UUID, name string) (File, error) {
	file := r.stored.File
	file.OriginalName = name
	return file, nil
}
func (r *fakeRepository) Move(_ context.Context, _, _ uuid.UUID, folderID *uuid.UUID) (File, error) {
	file := r.stored.File
	file.FolderID = folderID
	return file, nil
}
func (r *fakeRepository) BulkMove(_ context.Context, _ uuid.UUID, ids []uuid.UUID, folderID *uuid.UUID) ([]File, error) {
	result := make([]File, len(ids))
	for index, id := range ids {
		result[index] = File{ID: id, FolderID: folderID}
	}
	return result, nil
}
func (r *fakeRepository) BulkSoftDelete(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}
func (r *fakeRepository) SoftDelete(_ context.Context, _, _ uuid.UUID) error { return nil }
func (r *fakeRepository) ListTrash(_ context.Context, _ uuid.UUID, _, _ int) ([]TrashedFile, error) {
	return nil, nil
}
func (r *fakeRepository) GetTrashed(_ context.Context, _, _ uuid.UUID) (StoredFile, error) {
	return r.stored, nil
}
func (r *fakeRepository) Restore(_ context.Context, _, _ uuid.UUID) (File, error) {
	return r.stored.File, nil
}
func (r *fakeRepository) PermanentlyDelete(_ context.Context, _, _ uuid.UUID) error {
	r.permanentlyDeleted = true
	return nil
}
func (r *fakeRepository) TrashedVersionObjects(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]string, error) {
	return []string{r.stored.ObjectKey}, nil
}

func TestUploadRollsBackObjectWhenMetadataFails(t *testing.T) {
	store := &fakeStore{}
	repo := &fakeRepository{createErr: errors.New("database unavailable")}
	service := NewService(store, repo)

	_, err := service.Upload(context.Background(), uuid.New(), Upload{Name: "report.pdf", ContentType: "application/pdf", Size: 4, Body: strings.NewReader("data")})
	if err == nil {
		t.Fatal("expected upload error")
	}
	if store.deletedKey == "" || store.deletedKey != store.putKey {
		t.Fatalf("expected object rollback, put=%q deleted=%q", store.putKey, store.deletedKey)
	}
	if repo.reserved != 0 {
		t.Fatalf("expected reservation release, got %d", repo.reserved)
	}
}

func TestRenameRejectsPathTraversal(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeRepository{})
	_, err := service.Rename(context.Background(), uuid.New(), uuid.New(), "../secret.txt")
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("expected invalid name error, got %v", err)
	}
}

func TestRenameNormalizesUnicodeToNFC(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeRepository{})
	file, err := service.Rename(context.Background(), uuid.New(), uuid.New(), "Cafe\u0301.txt")
	if err != nil {
		t.Fatal(err)
	}
	if file.OriginalName != "Café.txt" {
		t.Fatalf("expected NFC name, got %q", file.OriginalName)
	}
}

func TestMoveSupportsRootAndFolderDestinations(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeRepository{})
	destination := uuid.New()
	file, err := service.Move(context.Background(), uuid.New(), uuid.New(), &destination)
	if err != nil || file.FolderID == nil || *file.FolderID != destination {
		t.Fatalf("expected destination %s, got %#v (%v)", destination, file.FolderID, err)
	}
	file, err = service.Move(context.Background(), uuid.New(), uuid.New(), nil)
	if err != nil || file.FolderID != nil {
		t.Fatalf("expected root destination, got %#v (%v)", file.FolderID, err)
	}
}

func TestBulkMoveRejectsEmptyAndDeduplicates(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeRepository{})
	if _, err := service.BulkMove(context.Background(), uuid.New(), nil, nil); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected invalid batch, got %v", err)
	}
	id := uuid.New()
	files, err := service.BulkMove(context.Background(), uuid.New(), []uuid.UUID{id, id}, nil)
	if err != nil || len(files) != 1 || files[0].ID != id {
		t.Fatalf("expected one deduplicated file, got %#v (%v)", files, err)
	}
}

func TestPermanentDeleteRemovesObjectBeforeMetadata(t *testing.T) {
	store := &fakeStore{}
	repo := &fakeRepository{stored: StoredFile{ObjectKey: "users/u/files/f"}}
	service := NewService(store, repo)
	if err := service.PermanentlyDelete(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if store.deletedKey != repo.stored.ObjectKey {
		t.Fatalf("expected object %q to be deleted, got %q", repo.stored.ObjectKey, store.deletedKey)
	}
	if !repo.permanentlyDeleted {
		t.Fatal("expected metadata to be permanently deleted")
	}
}

func TestDownloadDoesNotExposeObjectKey(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{presignURL: "https://storage.example/signed"}
	repo := &fakeRepository{stored: StoredFile{File: File{ID: id}, ObjectKey: "private/object-key"}}
	service := NewService(store, repo)

	download, err := service.Download(context.Background(), uuid.New(), id, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if download.URL != store.presignURL {
		t.Fatalf("unexpected URL %q", download.URL)
	}
}
