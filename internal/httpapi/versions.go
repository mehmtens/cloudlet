package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mehmtens/cloudlet/internal/files"
	"github.com/mehmtens/cloudlet/internal/versions"
)

func versionIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	fileID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file_id", "file id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	versionID, err := uuid.Parse(r.PathValue("version_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_version_id", "version id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	return fileID, versionID, true
}
func (a *API) uploadVersion(w http.ResponseWriter, r *http.Request) {
	if a.versions == nil {
		writeError(w, http.StatusServiceUnavailable, "versions_unavailable", "file versions are unavailable")
		return
	}
	fileID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file_id", "file id must be a UUID")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.maxUploadBytes)
	if err = r.ParseMultipartForm(a.maxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "expected multipart form data")
		return
	}
	stream, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "multipart field 'file' is required")
		return
	}
	defer stream.Close()
	version, err := a.versions.Upload(r.Context(), ownerID(r.Context()), fileID, versions.Upload{Body: stream, Size: header.Size, ContentType: header.Header.Get("Content-Type")})
	if errors.Is(err, files.ErrQuotaExceeded) {
		writeError(w, http.StatusConflict, "storage_quota_exceeded", err.Error())
		return
	}
	if errors.Is(err, files.ErrDisallowedType) {
		writeError(w, http.StatusUnsupportedMediaType, "file_type_not_allowed", err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file_not_found", "file was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "version_upload_failed", "file version could not be uploaded")
		return
	}
	a.triggerThumbnail(r.Context())
	writeJSON(w, http.StatusCreated, version)
}
func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	if a.versions == nil {
		writeError(w, http.StatusServiceUnavailable, "versions_unavailable", "file versions are unavailable")
		return
	}
	fileID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file_id", "file id must be a UUID")
		return
	}
	items, err := a.versions.List(r.Context(), ownerID(r.Context()), fileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "version_list_failed", "versions could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": items})
}
func (a *API) downloadVersion(w http.ResponseWriter, r *http.Request) {
	fileID, versionID, ok := versionIDs(w, r)
	if !ok {
		return
	}
	download, err := a.versions.Download(r.Context(), ownerID(r.Context()), fileID, versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "version_not_found", "version was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "version_download_failed", "download URL could not be created")
		return
	}
	writeJSON(w, http.StatusOK, download)
}
func (a *API) restoreVersion(w http.ResponseWriter, r *http.Request) {
	fileID, versionID, ok := versionIDs(w, r)
	if !ok {
		return
	}
	version, err := a.versions.Restore(r.Context(), ownerID(r.Context()), fileID, versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "version_not_found", "version was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "version_restore_failed", "version could not be restored")
		return
	}
	writeJSON(w, http.StatusOK, version)
}

func (a *API) deleteVersion(w http.ResponseWriter, r *http.Request) {
	fileID, versionID, ok := versionIDs(w, r)
	if !ok {
		return
	}
	err := a.versions.Delete(r.Context(), ownerID(r.Context()), fileID, versionID)
	if errors.Is(err, versions.ErrCurrentVersion) {
		writeError(w, http.StatusConflict, "current_version", "current version cannot be deleted")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "version_not_found", "version was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "version_delete_failed", "version could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
