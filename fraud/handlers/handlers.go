// Package handlers implements the HTTP endpoints for fraud prevention
// (roadmap Phase 18), RBAC-gated via the admin module's fraud.review
// permission.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/fraud"
	"github.com/lumena/fraud/fraudsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *fraudsvc.Service
}

func New(svc *fraudsvc.Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) GetDeviceCluster(w http.ResponseWriter, r *http.Request) {
	deviceHash := r.URL.Query().Get("device_hash")
	if deviceHash == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "device_hash is required.")
		return
	}
	accounts, err := h.svc.GetDeviceCluster(r.Context(), accountID(r), deviceHash)
	if err != nil {
		if errors.Is(err, fraud.ErrForbidden) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "fraud.review permission required.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not look up device cluster.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_hash": deviceHash, "account_ids": accounts})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
