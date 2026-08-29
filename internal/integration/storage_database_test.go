package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mehmtens/cloudlet/internal/database"
	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/storage"
)

func TestPostgresAndS3FileLifecycle(t *testing.T) {
	if os.Getenv("CLOUDLET_INTEGRATION_TESTS") != "1" {
		t.Skip("set CLOUDLET_INTEGRATION_TESTS=1 to run PostgreSQL/S3 integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, env("CLOUDLET_TEST_DATABASE_URL", "postgres://cloudlet:cloudlet@localhost:15432/cloudlet?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate PostgreSQL: %v", err)
	}

	store, err := storage.NewS3(ctx, storage.Config{
		Endpoint:       env("CLOUDLET_TEST_S3_ENDPOINT", "http://localhost:19000"),
		PublicEndpoint: env("CLOUDLET_TEST_S3_PUBLIC_ENDPOINT", "http://localhost:19000"),
		Region:         "us-east-1",
		Bucket:         env("CLOUDLET_TEST_S3_BUCKET", "cloudlet-integration"),
		AccessKey:      env("CLOUDLET_TEST_S3_ACCESS_KEY", "cloudlet"),
		SecretKey:      env("CLOUDLET_TEST_S3_SECRET_KEY", "cloudlet-secret"),
		UsePathStyle:   true,
	})
	if err != nil {
		t.Fatalf("connect S3-compatible storage: %v", err)
	}

	ownerID, otherID := uuid.New(), uuid.New()
	for _, user := range []struct {
		id    uuid.UUID
		email string
	}{{ownerID, "integration-" + ownerID.String() + "@example.com"}, {otherID, "integration-" + otherID.String() + "@example.com"}} {
		if _, err = db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,'integration-test')`, user.id, user.email); err != nil {
			t.Fatalf("create fixture user: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_, _ = db.Exec(cleanupCtx, `DELETE FROM users WHERE id=ANY($1)`, []uuid.UUID{ownerID, otherID})
	})

	service := files.NewService(store, files.NewPostgresRepository(db))
	payload := "Cloudlet PostgreSQL and S3 integration payload"
	created, err := service.Upload(ctx, ownerID, files.Upload{
		Name: "integration.txt", ContentType: "text/plain", Size: int64(len(payload)), Body: strings.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("upload file: %v", err)
	}

	got, err := service.Get(ctx, ownerID, created.ID)
	if err != nil {
		t.Fatalf("read owner metadata: %v", err)
	}
	if got.OriginalName != "integration.txt" || got.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if _, err = service.Get(ctx, otherID, created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-user read error = %v, want pgx.ErrNoRows", err)
	}

	download, err := service.Download(ctx, ownerID, created.ID, time.Minute)
	if err != nil {
		t.Fatalf("presign download: %v", err)
	}
	response, err := http.Get(download.URL)
	if err != nil {
		t.Fatalf("download object: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatalf("read downloaded object: %v", readErr)
	}
	if response.StatusCode != http.StatusOK || string(body) != payload {
		t.Fatalf("download status/body = %d/%q", response.StatusCode, body)
	}

	usage, err := service.Usage(ctx, ownerID)
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if usage.UsedBytes != int64(len(payload)) || usage.ReservedBytes != 0 {
		t.Fatalf("unexpected quota after upload: %+v", usage)
	}

	if err = service.Delete(ctx, ownerID, created.ID); err != nil {
		t.Fatalf("trash file: %v", err)
	}
	if err = service.PermanentlyDelete(ctx, ownerID, created.ID); err != nil {
		t.Fatalf("permanently delete file: %v", err)
	}
	usage, err = service.Usage(ctx, ownerID)
	if err != nil {
		t.Fatalf("read quota after delete: %v", err)
	}
	if usage.UsedBytes != 0 || usage.ReservedBytes != 0 {
		t.Fatalf("unexpected quota after delete: %+v", usage)
	}
	response, err = http.Get(download.URL)
	if err != nil {
		t.Fatalf("request deleted object: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted object status = %d, want 404", response.StatusCode)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
