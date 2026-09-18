// Package handlers implements the HTTP endpoints for the analytics
// dashboards (roadmap Phase 17), RBAC-gated via the admin module's
// analytics.view permission.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lumena/analytics"
	"github.com/lumena/analytics/analyticssvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *analyticssvc.Service
}

func New(svc *analyticssvc.Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) GetDAUMAU(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetDAUMAU(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handler) GetRetention(w http.ResponseWriter, r *http.Request) {
	cohort := time.Now().Add(-7 * 24 * time.Hour)
	if v := r.URL.Query().Get("cohort_date"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			cohort = t
		}
	}
	items, err := h.svc.GetRetention(r.Context(), accountID(r), cohort)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) GetFunnel(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.GetFunnel(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) GetEconomySummary(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("range"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	summary, err := h.svc.GetEconomySummary(r.Context(), accountID(r), days)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// GetCreatorAnalytics is doc 07 §12's GET /creator/analytics?range=.
func (h *Handler) GetCreatorAnalytics(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("range"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	items, err := h.svc.GetCreatorTrend(r.Context(), accountID(r), accountID(r), days)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, analytics.ErrForbidden) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Your role does not permit viewing analytics.")
		return
	}
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Something went wrong.")
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
