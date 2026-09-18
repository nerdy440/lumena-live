// Package handlers implements the HTTP endpoints for reports, the moderator
// console, enforcements/appeals (doc 07 §11, doc 03 MODR_001-3), and the
// dev-only moderator grant (roadmap Phase 15).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/moderation"
	"github.com/lumena/moderation/modsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *modsvc.Service
}

func New(svc *modsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── POST /reports ──────────────────────────────────────────────────────────

func (h *Handler) SubmitReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
		ReasonCode  string `json:"reason_code"`
		Detail      string `json:"detail"`
		EvidenceRef string `json:"evidence_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SubjectType == "" || req.SubjectID == "" || req.ReasonCode == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "subject_type, subject_id and reason_code are required.")
		return
	}
	rep, err := h.svc.SubmitReport(r.Context(), accountID(r), req.SubjectType, req.SubjectID, req.ReasonCode, req.Detail, req.EvidenceRef)
	if err != nil {
		if errors.Is(err, moderation.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not submit report.")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ─── GET /policies/rules ────────────────────────────────────────────────────

func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": h.svc.ListRules(r.Context())})
}

// ─── GET /me/enforcements, GET /me/enforcements/{id} ───────────────────────

func (h *Handler) ListMyEnforcements(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListMyEnforcements(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load enforcements.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) GetEnforcement(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.GetEnforcement(r.Context(), accountID(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// ─── POST /me/enforcements/{id}/appeal ──────────────────────────────────────

func (h *Handler) FileAppeal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement string `json:"statement"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	a, err := h.svc.FileAppeal(r.Context(), accountID(r), r.PathValue("id"), req.Statement)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// ─── Moderator console (moderator-only) ─────────────────────────────────────

func (h *Handler) ListQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListQueue(r.Context(), accountID(r), moderation.ReportState(r.URL.Query().Get("state")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) TriageReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.State == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "state is required.")
		return
	}
	rep, err := h.svc.TriageReport(r.Context(), accountID(r), r.PathValue("id"), moderation.ReportState(req.State))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) CreateEnforcement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID     string `json:"account_id"`
		RuleID        string `json:"rule_id"`
		Action        string `json:"action"`
		DurationHours int    `json:"duration_hours"`
		EvidenceRef   string `json:"evidence_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" || req.RuleID == "" || req.Action == "" || req.EvidenceRef == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "account_id, rule_id, action and evidence_ref are required.")
		return
	}
	modID := accountID(r)
	isMod, err := h.svc.IsModerator(r.Context(), modID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not verify moderator access.")
		return
	}
	if !isMod {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Moderator access required.")
		return
	}
	e, err := h.svc.CreateEnforcement(r.Context(), "human:"+modID, req.AccountID, req.RuleID, moderation.EnforcementAction(req.Action), req.DurationHours, req.EvidenceRef)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *Handler) ListAppealQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListAppealQueue(r.Context(), accountID(r), moderation.AppealState(r.URL.Query().Get("state")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) DecideAppeal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Uphold bool `json:"uphold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	a, err := h.svc.DecideAppeal(r.Context(), accountID(r), r.PathValue("id"), req.Uphold)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) ListRiskQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListRiskQueue(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─── POST /moderation/dev-grant — dev-only escalation, see
// modsvc.Service.DevGrantModerator's doc comment. NEVER ships to production.

func (h *Handler) DevGrantModerator(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DevGrantModerator(r.Context(), accountID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not grant moderator access.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, moderation.ErrNotModerator):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Moderator access required.")
	case errors.Is(err, moderation.ErrNotOwner):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "This record does not belong to you.")
	case errors.Is(err, moderation.ErrNotAppealable):
		writeError(w, http.StatusForbidden, "NOT_APPEALABLE", "This enforcement is not appealable.")
	case errors.Is(err, moderation.ErrAlreadyAppealed):
		writeError(w, http.StatusConflict, "ALREADY_APPEALED", err.Error())
	case errors.Is(err, moderation.ErrRuleNotFound):
		writeError(w, http.StatusBadRequest, "UNKNOWN_RULE", err.Error())
	case errors.Is(err, moderation.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "INVALID_ACTION", err.Error())
	case errors.Is(err, moderation.ErrAutomatedTerminationForbidden):
		writeError(w, http.StatusForbidden, "TERMINATION_REQUIRES_HUMAN", err.Error())
	case errors.Is(err, moderation.ErrReportNotFound), errors.Is(err, moderation.ErrEnforcementNotFound), errors.Is(err, moderation.ErrAppealNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Not found.")
	case errors.Is(err, moderation.ErrAppealAlreadyDecided):
		writeError(w, http.StatusConflict, "ALREADY_DECIDED", err.Error())
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
