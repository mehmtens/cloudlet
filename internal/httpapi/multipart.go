package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/storage"
	"github.com/mehmtens/cloudlet/internal/uploads"
)

func (a *API) startMultipart(w http.ResponseWriter, r *http.Request) {
	if a.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable", "multipart uploads are unavailable")
		return
	}
	var request struct {
		Name           string     `json:"name"`
		ContentType    string     `json:"content_type"`
		Checksum       string     `json:"checksum_sha256"`
		IdempotencyKey string     `json:"idempotency_key"`
		FolderID       *uuid.UUID `json:"folder_id"`
		Size           int64      `json:"size_bytes"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	started, err := a.uploads.Start(r.Context(), ownerID(r.Context()), uploads.Start{Name: request.Name, ContentType: request.ContentType, ChecksumSHA256: request.Checksum, IdempotencyKey: request.IdempotencyKey, FolderID: request.FolderID, Size: request.Size})
	if errors.Is(err, uploads.ErrInvalidUpload) {
		writeError(w, http.StatusBadRequest, "invalid_upload", err.Error())
		return
	}
	if errors.Is(err, files.ErrDisallowedType) {
		writeError(w, http.StatusUnsupportedMediaType, "disallowed_type", err.Error())
		return
	}
	if errors.Is(err, files.ErrQuotaExceeded) {
		writeError(w, http.StatusConflict, "storage_quota_exceeded", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "folder_not_found", "folder was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_start_failed", "multipart upload could not be started")
		return
	}
	writeJSON(w, http.StatusCreated, started)
}

func (a *API) completeMultipart(w http.ResponseWriter, r *http.Request) {
	if a.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable", "multipart uploads are unavailable")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload_id", "upload id must be a UUID")
		return
	}
	var request struct {
		Parts []storage.CompletedPart `json:"parts"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	file, err := a.uploads.Complete(r.Context(), ownerID(r.Context()), id, request.Parts)
	if errors.Is(err, uploads.ErrInvalidUpload) {
		writeError(w, http.StatusBadRequest, "invalid_upload", err.Error())
		return
	}
	if errors.Is(err, uploads.ErrUploadConflict) || errors.Is(err, uploads.ErrSizeMismatch) {
		writeError(w, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "upload_not_found", "upload was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_complete_failed", "multipart upload could not be completed")
		return
	}
	a.triggerThumbnail(r.Context())
	writeJSON(w, http.StatusCreated, file)
}

func (a *API) resumeMultipart(w http.ResponseWriter, r *http.Request) {
	if a.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable", "multipart uploads are unavailable")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload_id", "upload id must be a UUID")
		return
	}
	started, err := a.uploads.Resume(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, uploads.ErrUploadConflict) {
		writeError(w, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "upload_not_found", "upload was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_resume_failed", "multipart upload could not be resumed")
		return
	}
	writeJSON(w, http.StatusOK, started)
}

func (a *API) cancelMultipart(w http.ResponseWriter, r *http.Request) {
	if a.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable", "multipart uploads are unavailable")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload_id", "upload id must be a UUID")
		return
	}
	err = a.uploads.Cancel(r.Context(), ownerID(r.Context()), id)
	if errors.Is(err, uploads.ErrUploadConflict) {
		writeError(w, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "upload_not_found", "upload was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_cancel_failed", "multipart upload could not be cancelled")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
