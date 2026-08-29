package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/mehmtens/cloudlet/internal/auth"
)

func (a *API) closeAccount(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "account_closure_unavailable", "account closure is unavailable")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Password == "" {
		writeError(w, http.StatusBadRequest, "password_required", "current password is required")
		return
	}
	userID := ownerID(r.Context())
	if err := a.auth.VerifyPassword(r.Context(), userID, request.Password); errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "account_closure_failed", "account could not be closed")
		return
	}
	a.recordAudit(r, &userID, "account.closure_requested", "user", &userID, nil)
	if err := a.accounts.Close(r.Context(), userID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	} else if err != nil {
		slog.Error("close account", "user_id", userID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "account_closure_failed", "account could not be fully closed; please retry later")
		return
	}
	a.clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}
