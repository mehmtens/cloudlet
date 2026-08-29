package thumbnails

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "golang.org/x/image/webp"
)

var ErrUnavailable = errors.New("thumbnail unavailable")

type Store interface {
	Get(context.Context, string) (io.ReadCloser, error)
	Put(context.Context, string, string, io.Reader, int64) error
	Delete(context.Context, string) error
	PresignGet(context.Context, string, time.Duration) (string, error)
}

type Service struct {
	db           *pgxpool.Pool
	store        Store
	pdfToPPMPath string
}

func NewService(db *pgxpool.Pool, store Store, pdfToPPMPath string) *Service {
	return &Service{db: db, store: store, pdfToPPMPath: pdfToPPMPath}
}

func (s *Service) ProcessNext(ctx context.Context) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var versionID, ownerID, fileID uuid.UUID
	var sourceKey, contentType string
	err = tx.QueryRow(ctx, `SELECT id,owner_id,file_id,object_key,content_type FROM file_versions WHERE thumbnail_status='PENDING' OR (thumbnail_status='PROCESSING' AND thumbnail_started_at<NOW()-INTERVAL '10 minutes') ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&versionID, &ownerID, &fileID, &sourceKey, &contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE file_versions SET thumbnail_status='PROCESSING',thumbnail_started_at=NOW() WHERE id=$1`, versionID); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}

	thumbnail, err := s.render(ctx, sourceKey, contentType)
	if err != nil {
		_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE file_versions SET thumbnail_status='FAILED',thumbnail_started_at=NULL WHERE id=$1`, versionID)
		return true, err
	}
	key := "users/" + ownerID.String() + "/files/" + fileID.String() + "/thumbnails/" + versionID.String() + ".jpg"
	if err = s.store.Put(ctx, key, "image/jpeg", bytes.NewReader(thumbnail), int64(len(thumbnail))); err != nil {
		_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE file_versions SET thumbnail_status='PENDING',thumbnail_started_at=NULL WHERE id=$1`, versionID)
		return true, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE file_versions SET thumbnail_status='READY',thumbnail_object_key=$2,thumbnail_started_at=NULL WHERE id=$1 AND thumbnail_status='PROCESSING'`, versionID, key)
	if err != nil || tag.RowsAffected() == 0 {
		_ = s.store.Delete(context.WithoutCancel(ctx), key)
		if err == nil {
			err = ErrUnavailable
		}
		return true, err
	}
	return true, nil
}

func (s *Service) render(ctx context.Context, key, contentType string) ([]byte, error) {
	body, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 110<<20))
	if err != nil {
		return nil, err
	}
	if contentType == "application/pdf" {
		return s.renderPDF(ctx, data)
	}
	return renderImage(data)
}

func renderImage(data []byte) ([]byte, error) {
	configuration, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if configuration.Width <= 0 || configuration.Height <= 0 || int64(configuration.Width)*int64(configuration.Height) > 50_000_000 {
		return nil, errors.New("image dimensions exceed thumbnail safety limit")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	w, h := fit(bounds.Dx(), bounds.Dy(), 320, 240)
	target := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/w
			sy := bounds.Min.Y + y*bounds.Dy()/h
			target.Set(x, y, color.Color(source.At(sx, sy)))
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, target, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *Service) renderPDF(ctx context.Context, data []byte) ([]byte, error) {
	directory, err := os.MkdirTemp("", "cloudlet-pdf-thumbnail-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	inputPath := filepath.Join(directory, "source.pdf")
	if err := os.WriteFile(inputPath, data, 0600); err != nil {
		return nil, err
	}
	outputPrefix := filepath.Join(directory, "page")
	command := exec.CommandContext(ctx, s.pdfToPPMPath, "-f", "1", "-l", "1", "-singlefile", "-jpeg", "-scale-to", "320", inputPath, outputPrefix)
	if output, err := command.CombinedOutput(); err != nil {
		return nil, errors.New("PDF renderer failed: " + string(output))
	}
	return os.ReadFile(outputPrefix + ".jpg")
}

func fit(width, height, maxWidth, maxHeight int) (int, int) {
	if width <= 0 || height <= 0 {
		return 1, 1
	}
	if width <= maxWidth && height <= maxHeight {
		return width, height
	}
	if width*maxHeight > height*maxWidth {
		return maxWidth, max(1, height*maxWidth/width)
	}
	return max(1, width*maxHeight/height), maxHeight
}

func (s *Service) URL(ctx context.Context, ownerID, fileID uuid.UUID) (string, time.Time, error) {
	var key, status string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(v.thumbnail_object_key,''),v.thumbnail_status FROM active_files f JOIN file_versions v ON v.id=f.current_version_id WHERE f.id=$1 AND f.owner_id=$2`, fileID, ownerID).Scan(&key, &status)
	if err != nil {
		return "", time.Time{}, err
	}
	if status != "READY" || key == "" {
		return "", time.Time{}, ErrUnavailable
	}
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	url, err := s.store.PresignGet(ctx, key, 15*time.Minute)
	return url, expiresAt, err
}
