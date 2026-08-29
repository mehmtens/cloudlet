package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/folders"
	"github.com/mehmtens/cloudlet/internal/thumbnails"
	"github.com/mehmtens/cloudlet/internal/uploads"
	"github.com/mehmtens/cloudlet/internal/versions"
)

type CleanupExpiredUploadsArgs struct{}

func (CleanupExpiredUploadsArgs) Kind() string { return "cleanup_expired_uploads" }

type CleanupExpiredUploadsWorker struct {
	river.WorkerDefaults[CleanupExpiredUploadsArgs]
	Uploads *uploads.Service
}

func (w *CleanupExpiredUploadsWorker) Work(ctx context.Context, _ *river.Job[CleanupExpiredUploadsArgs]) error {
	_, err := w.Uploads.CleanupExpired(ctx, 100)
	return err
}

type PruneFileVersionsArgs struct{}

func (PruneFileVersionsArgs) Kind() string { return "prune_file_versions" }

type PruneFileVersionsWorker struct {
	river.WorkerDefaults[PruneFileVersionsArgs]
	Versions *versions.Service
}

func (w *PruneFileVersionsWorker) Work(ctx context.Context, _ *river.Job[PruneFileVersionsArgs]) error {
	_, err := w.Versions.PruneAll(ctx, 20, 100)
	return err
}

type EmptyExpiredTrashArgs struct{}

func (EmptyExpiredTrashArgs) Kind() string { return "empty_expired_trash" }

type EmptyExpiredTrashWorker struct {
	river.WorkerDefaults[EmptyExpiredTrashArgs]
	DB      *pgxpool.Pool
	Files   *files.Service
	Folders *folders.Service
}

type GenerateThumbnailsArgs struct{}

func (GenerateThumbnailsArgs) Kind() string { return "generate_thumbnails" }

type GenerateThumbnailsWorker struct {
	river.WorkerDefaults[GenerateThumbnailsArgs]
	Thumbnails *thumbnails.Service
}

func (w *GenerateThumbnailsWorker) Work(ctx context.Context, _ *river.Job[GenerateThumbnailsArgs]) error {
	for range 25 {
		processed, err := w.Thumbnails.ProcessNext(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func (w *EmptyExpiredTrashWorker) Work(ctx context.Context, _ *river.Job[EmptyExpiredTrashArgs]) error {
	folderRows, err := w.DB.Query(ctx, `SELECT owner_id,id FROM folders WHERE deleted_at<NOW()-INTERVAL '30 days' AND trash_batch_id=id ORDER BY deleted_at LIMIT 100`)
	if err != nil {
		return err
	}
	type target struct{ ownerID, id uuid.UUID }
	folderTargets := []target{}
	for folderRows.Next() {
		var t target
		if err := folderRows.Scan(&t.ownerID, &t.id); err != nil {
			folderRows.Close()
			return err
		}
		folderTargets = append(folderTargets, t)
	}
	err = folderRows.Err()
	folderRows.Close()
	if err != nil {
		return err
	}
	for _, t := range folderTargets {
		if err := w.Folders.PermanentlyDelete(ctx, t.ownerID, t.id); err != nil {
			return err
		}
	}
	fileRows, err := w.DB.Query(ctx, `SELECT owner_id,id FROM files WHERE deleted_at<NOW()-INTERVAL '30 days' ORDER BY deleted_at LIMIT 100`)
	if err != nil {
		return err
	}
	fileTargets := []target{}
	for fileRows.Next() {
		var t target
		if err := fileRows.Scan(&t.ownerID, &t.id); err != nil {
			fileRows.Close()
			return err
		}
		fileTargets = append(fileTargets, t)
	}
	err = fileRows.Err()
	fileRows.Close()
	if err != nil {
		return err
	}
	for _, t := range fileTargets {
		if err := w.Files.PermanentlyDelete(ctx, t.ownerID, t.id); err != nil {
			return err
		}
	}
	return nil
}

func New(ctx context.Context, db *pgxpool.Pool, uploadService *uploads.Service, versionService *versions.Service, fileService *files.Service, folderService *folders.Service, thumbnailService *thumbnails.Service) (*river.Client[pgx.Tx], error) {
	driver := riverpgxv5.New(db)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return nil, err
	}
	if _, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, err
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &CleanupExpiredUploadsWorker{Uploads: uploadService})
	river.AddWorker(workers, &PruneFileVersionsWorker{Versions: versionService})
	river.AddWorker(workers, &EmptyExpiredTrashWorker{DB: db, Files: fileService, Folders: folderService})
	river.AddWorker(workers, &GenerateThumbnailsWorker{Thumbnails: thumbnailService})
	return river.NewClient(driver, &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return CleanupExpiredUploadsArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return PruneFileVersionsArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) { return EmptyExpiredTrashArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return GenerateThumbnailsArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		},
	})
}
