package folders

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

var ErrInvalidName = errors.New("folder name must be between 1 and 255 characters")
var ErrNameConflict = errors.New("a folder with this name already exists")
var ErrCycle = errors.New("folder cannot be moved into itself or one of its descendants")

type Folder struct {
	ID        uuid.UUID  `json:"id"`
	ParentID  *uuid.UUID `json:"parent_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
}

type TrashedFolder struct {
	Folder
	DeletedAt time.Time `json:"deleted_at"`
}

type ObjectStore interface {
	Delete(context.Context, string) error
}

type Repository interface {
	Create(context.Context, uuid.UUID, Folder) error
	List(context.Context, uuid.UUID, *uuid.UUID) ([]Folder, error)
	Rename(context.Context, uuid.UUID, uuid.UUID, string) (Folder, error)
	Move(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (Folder, error)
	SoftDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	ListTrash(context.Context, uuid.UUID, int, int) ([]TrashedFolder, error)
	Restore(context.Context, uuid.UUID, uuid.UUID) (Folder, error)
	TrashObjects(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, []string, error)
	PermanentlyDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	Search(context.Context, uuid.UUID, string, int) ([]Folder, error)
}

type Service struct {
	repo  Repository
	store ObjectStore
}

func NewService(repo Repository, stores ...ObjectStore) *Service {
	service := &Service{repo: repo}
	if len(stores) > 0 {
		service.store = stores[0]
	}
	return service
}

func validateName(name string) (string, error) {
	name = norm.NFC.String(strings.TrimSpace(name))
	if name == "" || len([]byte(name)) > 255 || filepath.Base(name) != name {
		return "", ErrInvalidName
	}
	return name, nil
}

func (s *Service) Create(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID, name string) (Folder, error) {
	name, err := validateName(name)
	if err != nil {
		return Folder{}, err
	}
	folder := Folder{ID: uuid.New(), ParentID: parentID, Name: name, CreatedAt: time.Now().UTC()}
	if err := s.repo.Create(ctx, ownerID, folder); err != nil {
		return Folder{}, err
	}
	return folder, nil
}

func (s *Service) Rename(ctx context.Context, ownerID, id uuid.UUID, name string) (Folder, error) {
	name, err := validateName(name)
	if err != nil {
		return Folder{}, err
	}
	return s.repo.Rename(ctx, ownerID, id, name)
}

func (s *Service) Move(ctx context.Context, ownerID, id uuid.UUID, parentID *uuid.UUID) (Folder, error) {
	if parentID != nil && *parentID == id {
		return Folder{}, ErrCycle
	}
	return s.repo.Move(ctx, ownerID, id, parentID)
}

func (s *Service) Delete(ctx context.Context, ownerID, id uuid.UUID) error {
	return s.repo.SoftDelete(ctx, ownerID, id, uuid.New())
}

func (s *Service) ListTrash(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]TrashedFolder, error) {
	return s.repo.ListTrash(ctx, ownerID, limit, offset)
}

func (s *Service) Restore(ctx context.Context, ownerID, id uuid.UUID) (Folder, error) {
	return s.repo.Restore(ctx, ownerID, id)
}

func (s *Service) PermanentlyDelete(ctx context.Context, ownerID, id uuid.UUID) error {
	batchID, keys, err := s.repo.TrashObjects(ctx, ownerID, id)
	if err != nil {
		return err
	}
	if s.store == nil {
		return errors.New("object store is not configured")
	}
	for _, key := range keys {
		if err := s.store.Delete(ctx, key); err != nil {
			return err
		}
	}
	return s.repo.PermanentlyDelete(ctx, ownerID, id, batchID)
}

func (s *Service) List(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID) ([]Folder, error) {
	return s.repo.List(ctx, ownerID, parentID)
}

func (s *Service) Search(ctx context.Context, ownerID uuid.UUID, query string, limit int) ([]Folder, error) {
	query = norm.NFC.String(strings.TrimSpace(query))
	if query == "" {
		return []Folder{}, nil
	}
	return s.repo.Search(ctx, ownerID, query, limit)
}

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) Create(ctx context.Context, ownerID uuid.UUID, folder Folder) error {
	var err error
	pathSuffix := folder.ID.String() + "/"
	if folder.ParentID == nil {
		_, err = r.db.Exec(ctx, `INSERT INTO folders (id, owner_id, parent_id, name, path, created_at) VALUES ($1, $2, NULL, $3, $4, $5)`, folder.ID, ownerID, folder.Name, "/"+pathSuffix, folder.CreatedAt)
	} else {
		command, execErr := r.db.Exec(ctx, `INSERT INTO folders (id, owner_id, parent_id, name, path, created_at)
			SELECT $1, $2, id, $4, path || $5, $6 FROM active_folders WHERE id = $3 AND owner_id = $2`, folder.ID, ownerID, *folder.ParentID, folder.Name, pathSuffix, folder.CreatedAt)
		err = execErr
		if err == nil && command.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameConflict
	}
	return err
}

func (r *PostgresRepository) List(ctx context.Context, ownerID uuid.UUID, parentID *uuid.UUID) ([]Folder, error) {
	rows, err := r.db.Query(ctx, `SELECT id, parent_id, name, created_at FROM active_folders
		WHERE owner_id = $1 AND parent_id IS NOT DISTINCT FROM $2
		ORDER BY LOWER(name), id`, ownerID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Folder, 0)
	for rows.Next() {
		var folder Folder
		if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Search(ctx context.Context, ownerID uuid.UUID, query string, limit int) ([]Folder, error) {
	rows, err := r.db.Query(ctx, `SELECT id,parent_id,name,created_at FROM active_folders WHERE owner_id=$1 AND name ILIKE '%' || $2 || '%' ORDER BY similarity(name,$2) DESC,created_at DESC LIMIT $3`, ownerID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Folder{}
	for rows.Next() {
		var folder Folder
		if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Rename(ctx context.Context, ownerID, id uuid.UUID, name string) (Folder, error) {
	var folder Folder
	err := r.db.QueryRow(ctx, `UPDATE folders SET name=$3 WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL
		RETURNING id, parent_id, name, created_at`, id, ownerID, name).
		Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt)
	return folder, mapConflict(err)
}

func (r *PostgresRepository) Move(ctx context.Context, ownerID, id uuid.UUID, parentID *uuid.UUID) (Folder, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Folder{}, err
	}
	defer tx.Rollback(ctx)
	var oldPath string
	if err := tx.QueryRow(ctx, `SELECT path FROM active_folders WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&oldPath); err != nil {
		return Folder{}, err
	}
	newPrefix := "/" + id.String() + "/"
	if parentID != nil {
		var parentPath string
		if err := tx.QueryRow(ctx, `SELECT path FROM active_folders WHERE id=$1 AND owner_id=$2 FOR UPDATE`, *parentID, ownerID).Scan(&parentPath); err != nil {
			return Folder{}, err
		}
		if strings.HasPrefix(parentPath, oldPath) {
			return Folder{}, ErrCycle
		}
		newPrefix = parentPath + id.String() + "/"
	}
	var folder Folder
	err = tx.QueryRow(ctx, `UPDATE folders SET parent_id=$3 WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL
		RETURNING id, parent_id, name, created_at`, id, ownerID, parentID).
		Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt)
	if err = mapConflict(err); err != nil {
		return Folder{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE folders SET path=$2 || SUBSTRING(path FROM CHAR_LENGTH($3)+1) WHERE owner_id=$1 AND path LIKE $3 || '%'`, ownerID, newPrefix, oldPath); err != nil {
		return Folder{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Folder{}, err
	}
	return folder, nil
}

func mapConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameConflict
	}
	return err
}

func (r *PostgresRepository) SoftDelete(ctx context.Context, ownerID, id, batchID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `WITH RECURSIVE subtree AS (
		SELECT id FROM folders WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL
		UNION ALL SELECT f.id FROM folders f JOIN subtree s ON f.parent_id=s.id
		WHERE f.owner_id=$2 AND f.deleted_at IS NULL)
		UPDATE folders SET deleted_at=NOW(), trash_batch_id=$3 WHERE id IN (SELECT id FROM subtree)`, id, ownerID, batchID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE files SET deleted_at=NOW(), trash_batch_id=$2
		WHERE owner_id=$1 AND deleted_at IS NULL AND folder_id IN
		(SELECT id FROM folders WHERE owner_id=$1 AND trash_batch_id=$2)`, ownerID, batchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) ListTrash(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]TrashedFolder, error) {
	rows, err := r.db.Query(ctx, `SELECT id, parent_id, name, created_at, deleted_at FROM folders
		WHERE owner_id=$1 AND deleted_at IS NOT NULL ORDER BY deleted_at DESC, id DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TrashedFolder, 0)
	for rows.Next() {
		var folder TrashedFolder
		if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt, &folder.DeletedAt); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Restore(ctx context.Context, ownerID, id uuid.UUID) (Folder, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Folder{}, err
	}
	defer tx.Rollback(ctx)
	var batchID uuid.UUID
	var folder Folder
	if err := tx.QueryRow(ctx, `SELECT id, parent_id, name, created_at, trash_batch_id FROM folders
		WHERE id=$1 AND owner_id=$2 AND deleted_at IS NOT NULL`, id, ownerID).
		Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.CreatedAt, &batchID); err != nil {
		return Folder{}, err
	}
	if folder.ParentID != nil {
		var parentActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folders WHERE id=$1 AND owner_id=$2 AND (deleted_at IS NULL OR trash_batch_id=$3))`, *folder.ParentID, ownerID, batchID).Scan(&parentActive); err != nil {
			return Folder{}, err
		}
		if !parentActive {
			folder.ParentID = nil
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE folders SET parent_id=NULL WHERE id=$1 AND $2::uuid IS NULL`, id, folder.ParentID); err != nil {
		return Folder{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE folders SET deleted_at=NULL, trash_batch_id=NULL WHERE owner_id=$1 AND trash_batch_id=$2`, ownerID, batchID); err != nil {
		return Folder{}, mapConflict(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE files SET deleted_at=NULL, trash_batch_id=NULL WHERE owner_id=$1 AND trash_batch_id=$2`, ownerID, batchID); err != nil {
		return Folder{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Folder{}, err
	}
	return folder, nil
}

func (r *PostgresRepository) TrashObjects(ctx context.Context, ownerID, id uuid.UUID) (uuid.UUID, []string, error) {
	var batchID uuid.UUID
	if err := r.db.QueryRow(ctx, `SELECT trash_batch_id FROM folders WHERE id=$1 AND owner_id=$2 AND deleted_at IS NOT NULL`, id, ownerID).Scan(&batchID); err != nil {
		return uuid.Nil, nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT v.object_key FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.owner_id=$1 AND f.trash_batch_id=$2 UNION ALL SELECT v.thumbnail_object_key FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.owner_id=$1 AND f.trash_batch_id=$2 AND v.thumbnail_object_key IS NOT NULL`, ownerID, batchID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return uuid.Nil, nil, err
		}
		keys = append(keys, key)
	}
	return batchID, keys, rows.Err()
}

func (r *PostgresRepository) PermanentlyDelete(ctx context.Context, ownerID, id, batchID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var releasedBytes int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(v.size_bytes),0) FROM file_versions v JOIN files f ON f.id=v.file_id WHERE f.owner_id=$1 AND f.trash_batch_id=$2`, ownerID, batchID).Scan(&releasedBytes); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE owner_id=$1 AND trash_batch_id=$2`, ownerID, batchID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM folders WHERE owner_id=$1 AND trash_batch_id=$2`, ownerID, batchID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET used_bytes=GREATEST(0, used_bytes-$2) WHERE id=$1`, ownerID, releasedBytes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
