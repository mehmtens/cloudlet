package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mehmtens/cloudlet/internal/accounts"
	"github.com/mehmtens/cloudlet/internal/audit"
	"github.com/mehmtens/cloudlet/internal/auth"
	"github.com/mehmtens/cloudlet/internal/config"
	"github.com/mehmtens/cloudlet/internal/database"
	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/folders"
	"github.com/mehmtens/cloudlet/internal/httpapi"
	"github.com/mehmtens/cloudlet/internal/jobs"
	"github.com/mehmtens/cloudlet/internal/scanning"
	"github.com/mehmtens/cloudlet/internal/shares"
	"github.com/mehmtens/cloudlet/internal/storage"
	"github.com/mehmtens/cloudlet/internal/thumbnails"
	"github.com/mehmtens/cloudlet/internal/uploads"
	"github.com/mehmtens/cloudlet/internal/verification"
	"github.com/mehmtens/cloudlet/internal/versions"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("create database pool", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	if err := database.Migrate(ctx, db); err != nil {
		slog.Error("run database migrations", "error", err)
		os.Exit(1)
	}

	objectStore, err := storage.NewS3(ctx, cfg.S3)
	if err != nil {
		slog.Error("initialize object storage", "error", err)
		os.Exit(1)
	}
	var malwareScanner scanning.Scanner
	if cfg.ClamAVAddress != "" {
		malwareScanner = scanning.ClamAV{Address: cfg.ClamAVAddress, Timeout: cfg.ClamAVTimeout}
	}
	fileService := files.NewService(objectStore, files.NewPostgresRepository(db), malwareScanner)
	folderService := folders.NewService(folders.NewPostgresRepository(db), objectStore)
	shareService := shares.NewService(shares.NewPostgresRepository(db), objectStore, cfg.PublicBaseURL)
	authService := auth.NewServiceWithKeyRotation(auth.NewRepository(db), cfg.JWTKID, cfg.JWTSecret, cfg.JWTPreviousKID, cfg.JWTPreviousSecret, 15*time.Minute)
	if err := authService.ConfigureTOTPEncryption(cfg.TOTPEncryptionKey); err != nil {
		slog.Error("configure TOTP encryption", "error", err)
		os.Exit(1)
	}
	verificationService := verification.NewService(db, verification.NewSMTPSender(verification.SMTPConfig{Address: cfg.SMTPAddress, Host: cfg.SMTPHost, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, RequireTLS: cfg.SMTPRequireTLS}), cfg.PublicBaseURL)
	uploadService := uploads.NewService(uploads.NewRepository(db), objectStore, cfg.MaxUploadBytes, malwareScanner)
	versionService := versions.NewService(db, objectStore, cfg.MaxUploadBytes, malwareScanner)
	thumbnailService := thumbnails.NewService(db, objectStore, cfg.PDFToPPMPath)
	accountService := accounts.NewService(db, objectStore)
	jobClient, err := jobs.New(ctx, db, uploadService, versionService, fileService, folderService, thumbnailService)
	if err != nil {
		slog.Error("initialize River job queue", "error", err)
		os.Exit(1)
	}
	if err = jobClient.Start(ctx); err != nil {
		slog.Error("start River job queue", "error", err)
		os.Exit(1)
	}
	triggerThumbnails := func(ctx context.Context) error {
		_, err := jobClient.Insert(ctx, jobs.GenerateThumbnailsArgs{}, nil)
		return err
	}
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: httpapi.New(fileService, folderService, shareService, authService, cfg.MaxUploadBytes, httpapi.Options{CookieSecure: cfg.CookieSecure, Ready: db.Ping, Audit: audit.NewPostgresRecorder(db), Uploads: uploadService, Versions: versionService, Thumbnails: thumbnailService, TriggerThumbnails: triggerThumbnails, Verification: verificationService, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs, Accounts: accountService}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}

	shutdownSignal, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("Cloudlet API started", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	<-shutdownSignal.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	if err := jobClient.Stop(shutdownCtx); err != nil {
		slog.Error("stop River job queue", "error", err)
	}
}
