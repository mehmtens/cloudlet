package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mehmtens/cloudlet/internal/accounts"
	"github.com/mehmtens/cloudlet/internal/audit"
	"github.com/mehmtens/cloudlet/internal/auth"
	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/folders"
	"github.com/mehmtens/cloudlet/internal/shares"
	"github.com/mehmtens/cloudlet/internal/thumbnails"
	"github.com/mehmtens/cloudlet/internal/uploads"
	"github.com/mehmtens/cloudlet/internal/verification"
	"github.com/mehmtens/cloudlet/internal/versions"
)

type API struct {
	files             *files.Service
	folders           *folders.Service
	shares            *shares.Service
	auth              *auth.Service
	maxUploadBytes    int64
	cookieSecure      bool
	ready             func(context.Context) error
	limiter           *rateLimiter
	audit             audit.Recorder
	uploads           *uploads.Service
	versions          *versions.Service
	thumbnails        *thumbnails.Service
	triggerThumbnails func(context.Context) error
	verification      *verification.Service
	accounts          *accounts.Service
	requestCount      atomic.Uint64
	errorCount        atomic.Uint64
}

type Options struct {
	CookieSecure      bool
	Ready             func(context.Context) error
	Audit             audit.Recorder
	Uploads           *uploads.Service
	Versions          *versions.Service
	Thumbnails        *thumbnails.Service
	TriggerThumbnails func(context.Context) error
	Verification      *verification.Service
	TrustedProxyCIDRs []string
	Accounts          *accounts.Service
}

func New(fileService *files.Service, folderService *folders.Service, shareService *shares.Service, authService *auth.Service, maxUploadBytes int64, options ...Options) http.Handler {
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	api := &API{files: fileService, folders: folderService, shares: shareService, auth: authService, maxUploadBytes: maxUploadBytes, cookieSecure: option.CookieSecure, ready: option.Ready, limiter: newRateLimiter(option.TrustedProxyCIDRs...), audit: option.Audit, uploads: option.Uploads, versions: option.Versions, thumbnails: option.Thumbnails, triggerThumbnails: option.TriggerThumbnails, verification: option.Verification, accounts: option.Accounts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.health)
	mux.HandleFunc("GET /health/live", api.health)
	mux.HandleFunc("GET /health/ready", api.readiness)
	mux.HandleFunc("GET /metrics", api.metrics)
	mux.Handle("POST /v1/auth/register", api.rateLimit(5, time.Hour, func(r *http.Request) string { return "register:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.register)))
	mux.Handle("POST /v1/auth/login", api.rateLimit(5, time.Minute, func(r *http.Request) string { return "login:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.login)))
	mux.Handle("POST /v1/auth/email/verify", api.rateLimit(10, time.Minute, func(r *http.Request) string { return "email-verify:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.verifyEmail)))
	mux.Handle("POST /v1/auth/email/resend", api.requireAuth(api.rateLimit(3, time.Hour, func(r *http.Request) string { return "email-resend:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.resendVerification))))
	mux.Handle("GET /v1/auth/email/status", api.requireAuth(http.HandlerFunc(api.verificationStatus)))
	mux.Handle("POST /v1/auth/password/forgot", api.rateLimit(3, time.Hour, func(r *http.Request) string { return "password-forgot:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.forgotPassword)))
	mux.Handle("POST /v1/auth/password/reset", api.rateLimit(5, 15*time.Minute, func(r *http.Request) string { return "password-reset:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.resetPassword)))
	mux.Handle("POST /v1/auth/password/change", api.requireAuth(api.rateLimit(5, 15*time.Minute, func(r *http.Request) string { return "password-change:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.changePassword))))
	mux.Handle("GET /v1/auth/totp", api.requireAuth(http.HandlerFunc(api.totpStatus)))
	mux.Handle("POST /v1/auth/totp/setup", api.requireAuth(api.rateLimit(5, time.Hour, func(r *http.Request) string { return "totp-setup:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.totpSetup))))
	mux.Handle("POST /v1/auth/totp/enable", api.requireAuth(api.rateLimit(5, 15*time.Minute, func(r *http.Request) string { return "totp-enable:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.totpEnable))))
	mux.Handle("POST /v1/auth/totp/disable", api.requireAuth(api.rateLimit(5, 15*time.Minute, func(r *http.Request) string { return "totp-disable:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.totpDisable))))
	mux.HandleFunc("GET /v1/auth/csrf", api.csrf)
	mux.HandleFunc("POST /v1/auth/refresh", api.refresh)
	mux.HandleFunc("POST /v1/auth/logout", api.logout)
	mux.Handle("POST /v1/auth/logout-all", api.requireAuth(http.HandlerFunc(api.logoutAll)))
	mux.Handle("GET /v1/auth/sessions", api.requireAuth(http.HandlerFunc(api.listSessions)))
	mux.Handle("DELETE /v1/auth/sessions/{id}", api.requireAuth(http.HandlerFunc(api.revokeSession)))
	mux.Handle("DELETE /v1/auth/account", api.requireAuth(api.rateLimit(3, time.Hour, func(r *http.Request) string { return "account-close:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.closeAccount))))
	mux.Handle("POST /v1/files", api.requireAuth(http.HandlerFunc(api.uploadFile)))
	mux.Handle("POST /v1/uploads", api.requireAuth(api.rateLimit(60, time.Minute, func(r *http.Request) string { return "multipart:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.startMultipart))))
	mux.Handle("POST /v1/uploads/{id}/complete", api.requireAuth(http.HandlerFunc(api.completeMultipart)))
	mux.Handle("GET /v1/uploads/{id}", api.requireAuth(http.HandlerFunc(api.resumeMultipart)))
	mux.Handle("DELETE /v1/uploads/{id}", api.requireAuth(http.HandlerFunc(api.cancelMultipart)))
	mux.Handle("POST /v1/files/{id}/versions", api.requireAuth(http.HandlerFunc(api.uploadVersion)))
	mux.Handle("GET /v1/files/{id}/versions", api.requireAuth(http.HandlerFunc(api.listVersions)))
	mux.Handle("GET /v1/files/{id}/versions/{version_id}/download", api.requireAuth(http.HandlerFunc(api.downloadVersion)))
	mux.Handle("POST /v1/files/{id}/versions/{version_id}/restore", api.requireAuth(http.HandlerFunc(api.restoreVersion)))
	mux.Handle("DELETE /v1/files/{id}/versions/{version_id}", api.requireAuth(http.HandlerFunc(api.deleteVersion)))
	mux.Handle("GET /v1/files", api.requireAuth(http.HandlerFunc(api.listFiles)))
	mux.Handle("GET /v1/files/{id}", api.requireAuth(http.HandlerFunc(api.getFile)))
	mux.Handle("GET /v1/files/{id}/download", api.requireAuth(api.rateLimit(60, time.Minute, func(r *http.Request) string { return "presign:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.downloadFile))))
	mux.Handle("GET /v1/files/{id}/thumbnail", api.requireAuth(api.rateLimit(60, time.Minute, func(r *http.Request) string { return "thumbnail:" + ownerID(r.Context()).String() }, http.HandlerFunc(api.fileThumbnail))))
	mux.Handle("PATCH /v1/files/{id}", api.requireAuth(http.HandlerFunc(api.renameFile)))
	mux.Handle("POST /v1/files/{id}/move", api.requireAuth(http.HandlerFunc(api.moveFile)))
	mux.Handle("POST /v1/files/bulk/move", api.requireAuth(http.HandlerFunc(api.bulkMoveFiles)))
	mux.Handle("POST /v1/files/bulk/trash", api.requireAuth(http.HandlerFunc(api.bulkTrashFiles)))
	mux.Handle("DELETE /v1/files/{id}", api.requireAuth(http.HandlerFunc(api.deleteFile)))
	mux.Handle("GET /v1/trash", api.requireAuth(http.HandlerFunc(api.listTrash)))
	mux.Handle("POST /v1/trash/{id}/restore", api.requireAuth(http.HandlerFunc(api.restoreFile)))
	mux.Handle("DELETE /v1/trash/{id}", api.requireAuth(http.HandlerFunc(api.permanentlyDeleteFile)))
	mux.Handle("POST /v1/folders", api.requireAuth(http.HandlerFunc(api.createFolder)))
	mux.Handle("GET /v1/folders", api.requireAuth(http.HandlerFunc(api.listFolders)))
	mux.Handle("PATCH /v1/folders/{id}", api.requireAuth(http.HandlerFunc(api.renameFolder)))
	mux.Handle("POST /v1/folders/{id}/move", api.requireAuth(http.HandlerFunc(api.moveFolder)))
	mux.Handle("DELETE /v1/folders/{id}", api.requireAuth(http.HandlerFunc(api.deleteFolder)))
	mux.Handle("GET /v1/trash/folders", api.requireAuth(http.HandlerFunc(api.listFolderTrash)))
	mux.Handle("POST /v1/trash/folders/{id}/restore", api.requireAuth(http.HandlerFunc(api.restoreFolder)))
	mux.Handle("DELETE /v1/trash/folders/{id}", api.requireAuth(http.HandlerFunc(api.permanentlyDeleteFolder)))
	mux.Handle("GET /v1/storage", api.requireAuth(http.HandlerFunc(api.storageUsage)))
	mux.Handle("GET /v1/search", api.requireAuth(http.HandlerFunc(api.search)))
	mux.Handle("POST /v1/files/{id}/shares", api.requireAuth(http.HandlerFunc(api.createShare)))
	mux.Handle("GET /v1/files/{id}/shares", api.requireAuth(http.HandlerFunc(api.listShares)))
	mux.Handle("POST /v1/files/{id}/access", api.requireAuth(http.HandlerFunc(api.grantFileAccess)))
	mux.Handle("GET /v1/files/{id}/access", api.requireAuth(http.HandlerFunc(api.listFileAccess)))
	mux.Handle("PATCH /v1/file-access/{id}", api.requireAuth(http.HandlerFunc(api.updateFileAccess)))
	mux.Handle("DELETE /v1/file-access/{id}", api.requireAuth(http.HandlerFunc(api.revokeFileAccess)))
	mux.Handle("GET /v1/shared-with-me", api.requireAuth(http.HandlerFunc(api.listSharedWithMe)))
	mux.Handle("GET /v1/shared-with-me/{id}/download", api.requireAuth(http.HandlerFunc(api.downloadSharedFile)))
	mux.Handle("DELETE /v1/shares/{id}", api.requireAuth(http.HandlerFunc(api.revokeShare)))
	mux.Handle("POST /v1/public/shares/{token}/download", api.rateLimit(5, time.Minute, func(r *http.Request) string { return "share:" + api.limiter.clientIP(r) }, http.HandlerFunc(api.publicShareDownload)))
	return api.requestObservability(api.securityHeaders(mux))
}

func (a *API) rateLimit(limit int, window time.Duration, key func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.limiter.allow(key(r), limit, window) {
			w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (a *API) requestObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID, _ = randomToken()
		}
		w.Header().Set("X-Request-ID", requestID)
		started := time.Now()
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tracked, r)
		a.requestCount.Add(1)
		if tracked.status >= 500 {
			a.errorCount.Add(1)
		}
		slog.Info("http request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "status", tracked.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (a *API) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "# HELP cloudlet_http_requests_total Total HTTP requests handled.\n# TYPE cloudlet_http_requests_total counter\ncloudlet_http_requests_total %d\n# HELP cloudlet_http_errors_total Total HTTP 5xx responses.\n# TYPE cloudlet_http_errors_total counter\ncloudlet_http_errors_total %d\n", a.requestCount.Load(), a.errorCount.Load())
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "search_query_required", "q is required")
		return
	}
	limit := queryInt(r, "limit", 20)
	if limit < 1 || limit > 50 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 50")
		return
	}
	foundFiles, err := a.files.Search(r.Context(), ownerID(r.Context()), query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", "search could not be completed")
		return
	}
	foundFolders, err := a.folders.Search(r.Context(), ownerID(r.Context()), query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", "search could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "files": foundFiles, "folders": foundFolders})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		if a.cookieSecure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) storageUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := a.files.Usage(r.Context(), ownerID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_usage_failed", "storage usage could not be retrieved")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (a *API) createShare(w http.ResponseWriter, r *http.Request) {
	fileID, ok := parseID(w, r)
	if !ok {
		return
	}
	var request struct {
		Password        string     `json:"password"`
		ExpiresAt       *time.Time `json:"expires_at"`
		MaxAccessStarts *int64     `json:"max_access_starts"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	created, err := a.shares.Create(r.Context(), ownerID(r.Context()), fileID, request.Password, request.ExpiresAt, request.MaxAccessStarts)
	if errors.Is(err, shares.ErrEmailUnverified) {
		writeError(w, http.StatusForbidden, "email_verification_required", err.Error())
		return
	}
	if errors.Is(err, shares.ErrInvalidExpiry) || errors.Is(err, shares.ErrInvalidDownloadLimit) {
		writeError(w, http.StatusBadRequest, "invalid_share", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "share_create_failed", "share could not be created")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "share.created", "share", &created.Share.ID, map[string]any{"file_id": fileID})
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) listShares(w http.ResponseWriter, r *http.Request) {
	fileID, ok := parseID(w, r)
	if !ok {
		return
	}
	result, err := a.shares.List(r.Context(), ownerID(r.Context()), fileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "share_list_failed", "shares could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": result})
}

func (a *API) grantFileAccess(w http.ResponseWriter, r *http.Request) {
	fileID, ok := parseID(w, r)
	if !ok {
		return
	}
	var req struct {
		Email      string `json:"email"`
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &req) || strings.TrimSpace(req.Email) == "" {
		if req.Email == "" {
			writeError(w, http.StatusBadRequest, "email_required", "recipient email is required")
		}
		return
	}
	grant, err := a.shares.Grant(r.Context(), ownerID(r.Context()), fileID, req.Email, req.Permission)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "recipient_or_file_not_found", "file or recipient was not found")
		return
	}
	if errors.Is(err, shares.ErrAccessExists) {
		writeError(w, http.StatusConflict, "access_exists", "file is already shared with this user")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access_grant_failed", "file access could not be granted")
		return
	}
	if a.verification != nil {
		if notifyErr := a.verification.NotifyShare(context.WithoutCancel(r.Context()), strings.TrimSpace(req.Email), grant.FileName, grant.OwnerEmail); notifyErr != nil {
			slog.Warn("share notification failed", "error", notifyErr)
		}
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (a *API) listFileAccess(w http.ResponseWriter, r *http.Request) {
	fileID, ok := parseID(w, r)
	if !ok {
		return
	}
	result, err := a.shares.ListOwnedGrants(r.Context(), ownerID(r.Context()), fileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access_list_failed", "file access could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access": result})
}

func (a *API) listSharedWithMe(w http.ResponseWriter, r *http.Request) {
	result, err := a.shares.ListGranted(r.Context(), ownerID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "shared_list_failed", "shared files could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (a *API) updateFileAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req struct {
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	err := a.shares.UpdateGrant(r.Context(), ownerID(r.Context()), id, req.Permission)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "access_not_found", "access grant was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_permission", "permission must be read or write")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) revokeFileAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.shares.RevokeGrant(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "access_not_found", "access grant was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access_revoke_failed", "access grant could not be revoked")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) downloadSharedFile(w http.ResponseWriter, r *http.Request) {
	fileID, ok := parseID(w, r)
	if !ok {
		return
	}
	result, err := a.shares.DownloadGranted(r.Context(), ownerID(r.Context()), fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "shared_file_not_found", "shared file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "shared_download_failed", "shared file could not be downloaded")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) revokeShare(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.shares.Revoke(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "share_not_found", "share was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "share_revoke_failed", "share could not be revoked")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "share.revoked", "share", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) publicShareDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	var request struct {
		Password string `json:"password"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &request) {
		return
	}
	download, err := a.shares.Download(r.Context(), r.PathValue("token"), request.Password)
	if errors.Is(err, shares.ErrPasswordRequired) {
		writeError(w, http.StatusUnauthorized, "share_password_required", err.Error())
		return
	}
	if errors.Is(err, shares.ErrInvalidPassword) {
		writeError(w, http.StatusUnauthorized, "invalid_share_password", err.Error())
		return
	}
	if errors.Is(err, shares.ErrUnavailable) {
		writeError(w, http.StatusNotFound, "share_unavailable", "share is unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "share_download_failed", "download link could not be created")
		return
	}
	writeJSON(w, http.StatusOK, download)
}

func (a *API) deleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.folders.Delete(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_delete_failed", "folder could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listFolderTrash(w http.ResponseWriter, r *http.Request) {
	limit, offset := queryInt(r, "limit", 20), queryInt(r, "offset", 0)
	if limit < 1 || limit > 100 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset cannot be negative")
		return
	}
	result, err := a.folders.ListTrash(r.Context(), ownerID(r.Context()), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_trash_list_failed", "folder trash could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": result, "limit": limit, "offset": offset})
}

func (a *API) restoreFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	folder, err := a.folders.Restore(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "trashed folder was not found")
		return
	}
	if errors.Is(err, folders.ErrNameConflict) {
		writeError(w, http.StatusConflict, "folder_name_conflict", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_restore_failed", "folder could not be restored")
		return
	}
	writeJSON(w, http.StatusOK, folder)
}

func (a *API) permanentlyDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.folders.PermanentlyDelete(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "trashed folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_permanent_delete_failed", "folder could not be permanently deleted")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "folder.permanently_deleted", "folder", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) renameFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	folder, err := a.folders.Rename(r.Context(), ownerID(r.Context()), id, request.Name)
	writeFolderMutation(w, folder, err)
}

func (a *API) moveFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var request struct {
		ParentID *uuid.UUID `json:"parent_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	folder, err := a.folders.Move(r.Context(), ownerID(r.Context()), id, request.ParentID)
	writeFolderMutation(w, folder, err)
}

func writeFolderMutation(w http.ResponseWriter, folder folders.Folder, err error) {
	if errors.Is(err, folders.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "invalid_folder_name", err.Error())
		return
	}
	if errors.Is(err, folders.ErrCycle) {
		writeError(w, http.StatusConflict, "folder_cycle", err.Error())
		return
	}
	if errors.Is(err, folders.ErrNameConflict) {
		writeError(w, http.StatusConflict, "folder_name_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "folder or parent folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_update_failed", "folder could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, folder)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return false
	}
	return true
}

func (a *API) createFolder(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string     `json:"name"`
		ParentID *uuid.UUID `json:"parent_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	folder, err := a.folders.Create(r.Context(), ownerID(r.Context()), request.ParentID, request.Name)
	if errors.Is(err, folders.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "invalid_folder_name", err.Error())
		return
	}
	if errors.Is(err, folders.ErrNameConflict) {
		writeError(w, http.StatusConflict, "folder_name_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "parent_folder_not_found", "parent folder was not found")
		return
	}
	if err != nil {
		slog.Error("create folder failed", "error", err, "owner_id", ownerID(r.Context()))
		writeError(w, http.StatusInternalServerError, "folder_create_failed", "folder could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, folder)
}

func (a *API) listFolders(w http.ResponseWriter, r *http.Request) {
	var parentID *uuid.UUID
	if raw := r.URL.Query().Get("parent_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parent_id", "parent id must be a UUID")
			return
		}
		parentID = &parsed
	}
	result, err := a.folders.List(r.Context(), ownerID(r.Context()), parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "folder_list_failed", "folders could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": result, "parent_id": parentID})
}

func (a *API) listTrash(w http.ResponseWriter, r *http.Request) {
	limit, offset := queryInt(r, "limit", 20), queryInt(r, "offset", 0)
	if limit < 1 || limit > 100 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset cannot be negative")
		return
	}
	result, err := a.files.ListTrash(r.Context(), ownerID(r.Context()), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "trash_list_failed", "trash could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": result, "limit": limit, "offset": offset})
}

func (a *API) restoreFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	file, err := a.files.Restore(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "trashed file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "restore_failed", "file could not be restored")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (a *API) permanentlyDeleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.files.PermanentlyDelete(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "trashed file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "permanent_delete_failed", "file could not be permanently deleted")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "file.permanently_deleted", "file", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) renameFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	file, err := a.files.Rename(r.Context(), ownerID(r.Context()), id, request.Name)
	if errors.Is(err, files.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "invalid_file_name", err.Error())
		return
	}
	if errors.Is(err, files.ErrNameConflict) {
		writeError(w, http.StatusConflict, "file_name_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rename_failed", "file could not be renamed")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	err := a.files.Delete(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", "file could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) moveFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var request struct {
		FolderID *uuid.UUID `json:"folder_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	file, err := a.files.Move(r.Context(), ownerID(r.Context()), id, request.FolderID)
	if errors.Is(err, files.ErrNameConflict) {
		writeError(w, http.StatusConflict, "file_name_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_or_folder_not_found", "file or destination folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "file_move_failed", "file could not be moved")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

type bulkFileRequest struct {
	IDs      []uuid.UUID `json:"ids"`
	FolderID *uuid.UUID  `json:"folder_id"`
}

func (a *API) decodeBulkFileRequest(w http.ResponseWriter, r *http.Request) (bulkFileRequest, bool) {
	var request bulkFileRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid batch request")
		return request, false
	}
	return request, true
}

func (a *API) bulkMoveFiles(w http.ResponseWriter, r *http.Request) {
	request, ok := a.decodeBulkFileRequest(w, r)
	if !ok {
		return
	}
	filesMoved, err := a.files.BulkMove(r.Context(), ownerID(r.Context()), request.IDs, request.FolderID)
	if errors.Is(err, files.ErrInvalidBatch) {
		writeError(w, http.StatusBadRequest, "invalid_batch", err.Error())
		return
	}
	if errors.Is(err, files.ErrNameConflict) {
		writeError(w, http.StatusConflict, "file_name_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_or_folder_not_found", "file or destination folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "file_move_failed", "files could not be moved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": filesMoved})
}

func (a *API) bulkTrashFiles(w http.ResponseWriter, r *http.Request) {
	request, ok := a.decodeBulkFileRequest(w, r)
	if !ok {
		return
	}
	if err := a.files.BulkDelete(r.Context(), ownerID(r.Context()), request.IDs); errors.Is(err, files.ErrInvalidBatch) {
		writeError(w, http.StatusBadRequest, "invalid_batch", err.Error())
		return
	} else if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "one or more files were not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "bulk_delete_failed", "files could not be moved to trash")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	if a.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.ready(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "database is not ready")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) listFiles(w http.ResponseWriter, r *http.Request) {
	limit, offset := queryInt(r, "limit", 20), queryInt(r, "offset", 0)
	if limit < 1 || limit > 100 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
		return
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset cannot be negative")
		return
	}
	var folderID *uuid.UUID
	if raw := r.URL.Query().Get("folder_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_folder_id", "folder id must be a UUID")
			return
		}
		folderID = &parsed
	}
	sortBy, order := r.URL.Query().Get("sort"), r.URL.Query().Get("order")
	if sortBy == "" {
		sortBy = "created_at"
	}
	if order == "" {
		order = "desc"
	}
	if sortBy != "name" && sortBy != "size" && sortBy != "created_at" {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be name, size, or created_at")
		return
	}
	if order != "asc" && order != "desc" {
		writeError(w, http.StatusBadRequest, "invalid_order", "order must be asc or desc")
		return
	}
	var contentType *string
	if value := strings.TrimSpace(r.URL.Query().Get("content_type")); value != "" {
		contentType = &value
	}
	minSize, ok := optionalInt64(w, r, "min_size")
	if !ok {
		return
	}
	maxSize, ok := optionalInt64(w, r, "max_size")
	if !ok {
		return
	}
	if minSize != nil && maxSize != nil && *minSize > *maxSize {
		writeError(w, http.StatusBadRequest, "invalid_size_range", "min_size cannot exceed max_size")
		return
	}
	result, err := a.files.ListAdvanced(r.Context(), ownerID(r.Context()), files.ListOptions{FolderID: folderID, Limit: limit, Offset: offset, Sort: sortBy, Order: order, ContentType: contentType, MinSize: minSize, MaxSize: maxSize})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", "files could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": result, "limit": limit, "offset": offset})
}
func optionalInt64(w http.ResponseWriter, r *http.Request, key string) (*int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		writeError(w, http.StatusBadRequest, "invalid_"+key, key+" must be a non-negative integer")
		return nil, false
	}
	return &value, true
}

func (a *API) getFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	file, err := a.files.Get(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get_failed", "file could not be retrieved")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (a *API) downloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	download, err := a.files.Download(r.Context(), ownerID(r.Context()), id, 15*time.Minute)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "download_failed", "download link could not be created")
		return
	}
	writeJSON(w, http.StatusOK, download)
}

func (a *API) fileThumbnail(w http.ResponseWriter, r *http.Request) {
	if a.thumbnails == nil {
		writeError(w, http.StatusServiceUnavailable, "thumbnails_unavailable", "thumbnail service is unavailable")
		return
	}
	fileID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file_id", "file id must be a UUID")
		return
	}
	url, expiresAt, err := a.thumbnails.URL(r.Context(), ownerID(r.Context()), fileID)
	if errors.Is(err, thumbnails.ErrUnavailable) {
		writeError(w, http.StatusNotFound, "thumbnail_not_ready", "thumbnail is not available yet")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "thumbnail_failed", "thumbnail link could not be created")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "expires_at": expiresAt})
}

func (a *API) uploadFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxUploadBytes)
	if err := r.ParseMultipartForm(a.maxUploadBytes); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds upload limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_multipart", "expected multipart form data")
		return
	}
	stream, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "multipart field 'file' is required")
		return
	}
	defer stream.Close()
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var folderID *uuid.UUID
	if raw := r.FormValue("folder_id"); raw != "" {
		parsed, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_folder_id", "folder id must be a UUID")
			return
		}
		folderID = &parsed
	}
	file, err := a.files.Upload(r.Context(), ownerID(r.Context()), files.Upload{Name: header.Filename, ContentType: contentType, Size: header.Size, Body: stream, FolderID: folderID})
	if errors.Is(err, files.ErrQuotaExceeded) {
		writeError(w, http.StatusConflict, "storage_quota_exceeded", "storage quota exceeded")
		return
	}
	if errors.Is(err, files.ErrNameConflict) {
		writeError(w, http.StatusConflict, "file_name_conflict", err.Error())
		return
	}
	if errors.Is(err, files.ErrInvalidSize) {
		writeError(w, http.StatusBadRequest, "invalid_file_size", err.Error())
		return
	}
	if errors.Is(err, files.ErrDisallowedType) {
		writeError(w, http.StatusUnsupportedMediaType, "file_type_not_allowed", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "folder was not found")
		return
	}
	if err != nil {
		slog.Error("upload failed", "error", err, "owner_id", ownerID(r.Context()), "file_name", header.Filename)
		writeError(w, http.StatusInternalServerError, "upload_failed", "file could not be uploaded")
		return
	}
	a.triggerThumbnail(r.Context())
	writeJSON(w, http.StatusCreated, file)
}

func (a *API) triggerThumbnail(ctx context.Context) {
	if a.triggerThumbnails != nil {
		if err := a.triggerThumbnails(ctx); err != nil {
			slog.Warn("thumbnail job enqueue failed", "error", err)
		}
	}
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code,omitempty"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	user, tokens, err := a.auth.RegisterWithSession(r.Context(), request.Email, request.Password)
	if errors.Is(err, auth.ErrEmailTaken) {
		writeError(w, http.StatusConflict, "email_taken", "email is already registered")
		return
	}
	if errors.Is(err, auth.ErrInvalidEmail) || errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, "invalid_registration", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "registration_failed", "registration could not be completed")
		return
	}
	a.setAuthCookies(w, tokens)
	emailSent := false
	if a.verification != nil {
		if err := a.verification.Issue(r.Context(), user.ID, user.Email); err != nil {
			slog.Error("send verification email", "user_id", user.ID, "error", err)
		} else {
			emailSent = true
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "verification_email_sent": emailSent})
}

func (a *API) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if a.verification == nil {
		writeError(w, http.StatusServiceUnavailable, "verification_unavailable", "email verification is unavailable")
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	if err := a.verification.Verify(r.Context(), request.Token); errors.Is(err, verification.ErrInvalidToken) {
		writeError(w, http.StatusBadRequest, "invalid_verification_token", err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "verification_failed", "email could not be verified")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}

func (a *API) resendVerification(w http.ResponseWriter, r *http.Request) {
	if a.verification == nil {
		writeError(w, http.StatusServiceUnavailable, "verification_unavailable", "email verification is unavailable")
		return
	}
	err := a.verification.Resend(r.Context(), ownerID(r.Context()))
	if errors.Is(err, verification.ErrAlreadyVerified) {
		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
		return
	}
	if err != nil {
		slog.Error("resend verification email", "user_id", ownerID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "verification_send_failed", "verification email could not be sent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) verificationStatus(w http.ResponseWriter, r *http.Request) {
	if a.verification == nil {
		writeError(w, http.StatusServiceUnavailable, "verification_unavailable", "email verification is unavailable")
		return
	}
	verified, err := a.verification.Status(r.Context(), ownerID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verification_status_failed", "verification status could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": verified})
}

func (a *API) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if a.verification == nil {
		writeError(w, http.StatusServiceUnavailable, "password_reset_unavailable", "password reset is unavailable")
		return
	}
	var request struct {
		Email string `json:"email"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Email) == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "a valid request body is required")
		return
	}
	if err := a.verification.RequestPasswordReset(r.Context(), request.Email); err != nil {
		// Keep the response identical for existing and unknown addresses.
		slog.Error("request password reset", "error", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) resetPassword(w http.ResponseWriter, r *http.Request) {
	if a.verification == nil {
		writeError(w, http.StatusServiceUnavailable, "password_reset_unavailable", "password reset is unavailable")
		return
	}
	var request struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	err := a.verification.ResetPassword(r.Context(), request.Token, request.Password)
	if errors.Is(err, verification.ErrInvalidResetToken) {
		writeError(w, http.StatusBadRequest, "invalid_reset_token", err.Error())
		return
	}
	if errors.Is(err, verification.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_reset_failed", "password could not be reset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"current_password"`
		Password        string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.CurrentPassword == "" || request.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "current_password and password are required")
		return
	}
	actor := ownerID(r.Context())
	err := a.auth.ChangePassword(r.Context(), actor, request.CurrentPassword, request.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_change_failed", "password could not be changed")
		return
	}
	a.clearAuthCookies(w)
	a.recordAudit(r, &actor, "password.changed", "user", &actor, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) totpStatus(w http.ResponseWriter, r *http.Request) {
	enabled, err := a.auth.TOTPStatus(r.Context(), ownerID(r.Context()))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "totp_unavailable", "TOTP is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

func (a *API) totpSetup(w http.ResponseWriter, r *http.Request) {
	userID := ownerID(r.Context())
	secret, err := a.auth.BeginTOTP(r.Context(), userID)
	if errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		writeError(w, http.StatusConflict, "totp_already_enabled", "TOTP is already enabled")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "totp_setup_failed", "TOTP setup could not be started")
		return
	}
	user, err := a.auth.UserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "totp_setup_failed", "TOTP setup could not be started")
		return
	}
	label := url.QueryEscape("Cloudlet:" + user.Email)
	issuer := url.QueryEscape("Cloudlet")
	a.recordAudit(r, &userID, "totp.setup.started", "user", &userID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_url": "otpauth://totp/" + label + "?secret=" + secret + "&issuer=" + issuer})
}

func (a *API) totpEnable(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&request) != nil || strings.TrimSpace(request.Code) == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "code is required")
		return
	}
	if err := a.auth.EnableTOTP(r.Context(), ownerID(r.Context()), request.Code); err != nil {
		actor := ownerID(r.Context())
		a.recordAudit(r, &actor, "totp.enable_failed", "user", &actor, nil)
		writeError(w, http.StatusBadRequest, "invalid_totp_code", "TOTP code is invalid")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "totp.enabled", "user", &actor, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
}

func (a *API) totpDisable(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&request) != nil || strings.TrimSpace(request.Code) == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "code is required")
		return
	}
	if err := a.auth.DisableTOTP(r.Context(), ownerID(r.Context()), request.Code); err != nil {
		actor := ownerID(r.Context())
		a.recordAudit(r, &actor, "totp.disable_failed", "user", &actor, nil)
		writeError(w, http.StatusBadRequest, "invalid_totp_code", "TOTP code is invalid")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "totp.disabled", "user", &actor, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	user, tokens, err := a.auth.LoginWithSessionCode(r.Context(), request.Email, request.Password, request.TOTPCode)
	if errors.Is(err, auth.ErrTOTPRequired) {
		a.recordAudit(r, nil, "login.totp_required", "user", nil, map[string]any{"email": strings.ToLower(strings.TrimSpace(request.Email))})
		writeError(w, http.StatusUnauthorized, "totp_required", "authenticator code is required")
		return
	}
	if errors.Is(err, auth.ErrInvalidTOTP) {
		a.recordAudit(r, nil, "login.totp_failed", "user", nil, map[string]any{"email": strings.ToLower(strings.TrimSpace(request.Email))})
		writeError(w, http.StatusUnauthorized, "invalid_totp", "authenticator code is invalid")
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		a.recordAudit(r, nil, "login.failed", "user", nil, map[string]any{"email": strings.ToLower(strings.TrimSpace(request.Email))})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login_failed", "login could not be completed")
		return
	}
	a.setAuthCookies(w, tokens)
	a.recordAudit(r, &user.ID, "login.succeeded", "user", &user.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

const accessCookieName = "cloudlet_access"
const refreshCookieName = "cloudlet_refresh"
const csrfCookieName = "XSRF-TOKEN"

func (a *API) csrf(w http.ResponseWriter, _ *http.Request) {
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "csrf_failed", "CSRF token could not be created")
		return
	}
	a.setCSRFCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}

func (a *API) setAuthCookies(w http.ResponseWriter, tokens auth.Tokens) {
	http.SetCookie(w, &http.Cookie{Name: accessCookieName, Value: tokens.AccessToken, Path: "/", HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 15 * 60})
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: tokens.RefreshToken, Path: "/", HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
	csrf, err := randomToken()
	if err == nil {
		a.setCSRFCookie(w, csrf)
	}
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	raw, cookieBased := refreshTokenFromRequest(w, r)
	if raw == "" {
		return
	}
	if cookieBased && !validCSRF(r) {
		writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF token is missing or invalid")
		return
	}
	tokens, err := a.auth.Refresh(r.Context(), raw)
	if errors.Is(err, auth.ErrInvalidRefreshToken) {
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "refresh_failed", "session could not be refreshed")
		return
	}
	a.setAuthCookies(w, tokens)
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	raw, cookieBased := refreshTokenFromRequest(w, r)
	if raw == "" {
		return
	}
	if cookieBased && !validCSRF(r) {
		writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF token is missing or invalid")
		return
	}
	_ = a.auth.Logout(r.Context(), raw)
	a.recordAudit(r, nil, "logout", "session", nil, nil)
	a.clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.LogoutAll(r.Context(), ownerID(r.Context())); err != nil {
		writeError(w, http.StatusInternalServerError, "logout_failed", "sessions could not be revoked")
		return
	}
	a.clearAuthCookies(w)
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "logout.all", "user", &actor, nil)
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.auth.Sessions(r.Context(), ownerID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_list_failed", "sessions could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}
func (a *API) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "session id must be a UUID")
		return
	}
	err = a.auth.RevokeSessionByID(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session_not_found", "session was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_revoke_failed", "session could not be revoked")
		return
	}
	actor := ownerID(r.Context())
	a.recordAudit(r, &actor, "session.revoked", "session", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) recordAudit(r *http.Request, actor *uuid.UUID, eventType, targetType string, targetID *uuid.UUID, metadata map[string]any) {
	if a.audit == nil {
		return
	}
	if err := a.audit.Record(context.WithoutCancel(r.Context()), audit.Event{ActorUserID: actor, Type: eventType, TargetType: targetType, TargetID: targetID, IP: a.limiter.clientIP(r), Metadata: metadata}); err != nil {
		slog.Error("audit record failed", "error", err, "event_type", eventType)
	}
}
func refreshTokenFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		return cookie.Value, true
	}
	writeError(w, http.StatusUnauthorized, "refresh_cookie_required", "refresh session cookie is required")
	return "", false
}
func (a *API) clearAuthCookies(w http.ResponseWriter) {
	for _, cookie := range []*http.Cookie{{Name: accessCookieName, Path: "/"}, {Name: refreshCookieName, Path: "/"}, {Name: csrfCookieName, Path: "/"}} {
		cookie.Value = ""
		cookie.MaxAge = -1
		cookie.HttpOnly = cookie.Name != csrfCookieName
		cookie.Secure = a.cookieSecure
		cookie.SameSite = http.SameSiteLaxMode
		http.SetCookie(w, cookie)
	}
}
func (a *API) setCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", HttpOnly: false, Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 24 * 60 * 60})
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentialsRequest, bool) {
	var request credentialsRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return request, false
	}
	return request, true
}

type ownerContextKey struct{}

func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		rawToken := ""
		bearer := strings.HasPrefix(header, "Bearer ")
		if bearer {
			rawToken = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		} else if cookie, err := r.Cookie(accessCookieName); err == nil {
			rawToken = cookie.Value
		}
		if rawToken == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "access token is required")
			return
		}
		id, err := a.auth.ParseToken(rawToken)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "access token is invalid or expired")
			return
		}
		if !bearer && isUnsafeMethod(r.Method) && !validCSRF(r) {
			writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF token is missing or invalid")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerContextKey{}, id)))
	})
}
func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}
func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	header := r.Header.Get("X-CSRF-Token")
	return header != "" && len(header) == len(cookie.Value) && subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}
func ownerID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(ownerContextKey{}).(uuid.UUID)
	return id
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file_id", "file id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}
func queryInt(r *http.Request, key string, fallback int) int {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
