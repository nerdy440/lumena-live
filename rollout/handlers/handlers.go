// Package handlers implements the HTTP endpoints for feature flags,
// staged rollout, and canary metrics (roadmap Phase 20, doc 07's GET
// /config).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/rollout"
	"github.com/lumena/rollout/rolloutsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc   *rolloutsvc.Service
	flags []string // the set of flag keys /config evaluates and reports
}

func New(svc *rolloutsvc.Service, flagKeys []string) *Handler {
	return &Handler{svc: svc, flags: flagKeys}
}

// GetConfig is doc 07 §13's GET /config — feature flags evaluated for the
// calling account (or anonymous), plus static min-version/region fields.
// No client versioning or region-availability system exists in this dev
// build, so those two are honest static placeholders, not derived data.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	evaluated := make(map[string]bool, len(h.flags))
	for _, key := range h.flags {
		enabled, _ := h.svc.Evaluate(r.Context(), key, acct)
		evaluated[key] = enabled
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feature_flags":        evaluated,
		"min_version":          "1.0.0",
		"region_availability":  []string{"US", "PK", "IN", "BR", "NG", "PH", "ID", "MX", "EG", "TR"},
	})
}

func (h *Handler) ListFlags(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListFlags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load flags.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) SetStage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Stage string `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" || req.Stage == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "key and stage are required.")
		return
	}
	f, err := h.svc.SetStage(r.Context(), accountID(r), req.Key, rollout.Stage(req.Stage))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *Handler) GetCanaryMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.GetCanaryMetrics(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// DevMarkInternal — see rolloutsvc.Service.DevMarkInternal's doc comment.
// NEVER ships to production.
func (h *Handler) DevMarkInternal(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DevMarkInternal(r.Context(), accountID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not mark internal.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rollout.ErrForbidden):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "rollout.manage permission required.")
	case errors.Is(err, rollout.ErrInvalidStage):
		writeError(w, http.StatusBadRequest, "INVALID_STAGE", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Something went wrong.")
	}
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
