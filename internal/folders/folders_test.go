package folders

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeRepository struct {
	created Folder
	folders []Folder
}

func (r *fakeRepository) Create(_ context.Context, _ uuid.UUID, folder Folder) error {
	r.created = folder
	return nil
}
func (r *fakeRepository) List(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]Folder, error) {
	return r.folders, nil
}
func (r *fakeRepository) Search(_ context.Context, _ uuid.UUID, _ string, _ int) ([]Folder, error) {
	return []Folder{}, nil
}
func (r *fakeRepository) Rename(_ context.Context, _, id uuid.UUID, name string) (Folder, error) {
	return Folder{ID: id, Name: name}, nil
}
func (r *fakeRepository) Move(_ context.Context, _, id uuid.UUID, parentID *uuid.UUID) (Folder, error) {
	return Folder{ID: id, ParentID: parentID}, nil
}
func (r *fakeRepository) SoftDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeRepository) ListTrash(context.Context, uuid.UUID, int, int) ([]TrashedFolder, error) {
	return nil, nil
}
func (r *fakeRepository) Restore(_ context.Context, _, id uuid.UUID) (Folder, error) {
	return Folder{ID: id}, nil
}
func (r *fakeRepository) TrashObjects(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, []string, error) {
	return uuid.New(), nil, nil
}
func (r *fakeRepository) PermanentlyDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}

func TestCreateNestedFolder(t *testing.T) {
	parentID := uuid.New()
	repo := &fakeRepository{}
	service := NewService(repo)
	folder, err := service.Create(context.Background(), uuid.New(), &parentID, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if folder.ParentID == nil || *folder.ParentID != parentID {
		t.Fatal("expected parent folder to be retained")
	}
	if repo.created.Name != "Projects" {
		t.Fatalf("unexpected name %q", repo.created.Name)
	}
}

func TestCreateRejectsPathTraversal(t *testing.T) {
	service := NewService(&fakeRepository{})
	_, err := service.Create(context.Background(), uuid.New(), nil, "../private")
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("expected invalid name, got %v", err)
	}
}

func TestCreateNormalizesUnicodeToNFC(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(repo)
	folder, err := service.Create(context.Background(), uuid.New(), nil, "Cafe\u0301")
	if err != nil {
		t.Fatal(err)
	}
	if folder.Name != "Café" {
		t.Fatalf("expected NFC name, got %q", folder.Name)
	}
}

func TestMoveRejectsSelfParent(t *testing.T) {
	service := NewService(&fakeRepository{})
	id := uuid.New()
	_, err := service.Move(context.Background(), uuid.New(), id, &id)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("expected cycle error, got %v", err)
	}
}
