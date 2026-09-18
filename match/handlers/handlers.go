// Package handlers implements the HTTP endpoints for matchmaking (doc 07
// §10, roadmap Phase 12). Every response builder here hand-picks fields
// rather than passing a match.Candidate straight through, so score_reasons
// is excluded even if the struct's json:"-" tag were ever loosened —
// defense in depth for the roadmap exit gate ("score_reasons is not in
// any API response").
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/match"
	"github.com/lumena/match/matchsvc"
	"github.com/lumena/shared/ctxkeys"
)

func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc   *matchsvc.Service
	prefs match.PreferencesRepo
}

func New(svc *matchsvc.Service, prefs match.PreferencesRepo) *Handler {
	return &Handler{svc: svc, prefs: prefs}
}

// ─── GET /me/match-preferences ────────────────────────────────────────────────

func (h *Handler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	prefs, err := h.prefs.Get(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load match preferences.")
		return
	}
	writeJSON(w, http.StatusOK, prefs)
}

// ─── PATCH /me/match-preferences ──────────────────────────────────────────────

func (h *Handler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var prefs match.Preferences
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	if err := h.prefs.Set(r.Context(), accountID, prefs); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not save match preferences.")
		return
	}
	writeJSON(w, http.StatusOK, prefs)
}

// ─── POST /match/requests {preferences} ──────────────────────────────────────

func (h *Handler) CreateRequest(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		Preferences match.Preferences `json:"preferences"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	prefs := req.Preferences
	if prefs == (match.Preferences{}) {
		if stored, err := h.prefs.Get(r.Context(), accountID); err == nil {
			prefs = stored
		}
	}

	mreq, err := h.svc.CreateRequest(r.Context(), accountID, prefs)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeRequestWithCandidate(w, accountID, mreq)
}

// ─── GET /match/requests/{id} ─────────────────────────────────────────────────

func (h *Handler) GetRequest(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	mreq, err := h.svc.GetRequest(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeRequestWithCandidate(w, accountID, mreq)
}

// ─── DELETE /match/requests/{id} ─────────────────────────────────────────────

func (h *Handler) CancelRequest(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if err := h.svc.CancelRequest(r.Context(), accountID, r.PathValue("id")); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── POST /match/decisions {request_id, candidate_id, decision} ─────────────

func (h *Handler) Decide(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		RequestID   string `json:"request_id"`
		CandidateID string `json:"candidate_id"`
		Decision    string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" || req.CandidateID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request_id, candidate_id and decision are required.")
		return
	}
	mreq, err := h.svc.Decide(r.Context(), accountID, req.RequestID, req.CandidateID, match.Decision(req.Decision))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeRequestWithCandidate(w, accountID, mreq)
}

// ─── GET /match/history?cursor= ──────────────────────────────────────────────
// cursor is accepted but this dev build returns the full (small) history in
// one page — no pagination-scale data exists to page through.

func (h *Handler) ListHistory(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	history, err := h.svc.ListHistory(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load match history.")
		return
	}
	items := make([]map[string]any, 0, len(history))
	for i := range history {
		items = append(items, requestResponse(&history[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ── helpers ─────────────────────────────────────────────────────────────────

// writeRequestWithCandidate includes the pending candidate (if any) using
// an explicit field allow-list — id, score, offered_at — never the
// internal-only score_reasons.
func (h *Handler) writeRequestWithCandidate(w http.ResponseWriter, accountID string, mreq *match.Request) {
	resp := requestResponse(mreq)
	if mreq.State == match.StateSearching {
		cand, err := h.svc.GetPendingCandidate(context.Background(), accountID, mreq.ID)
		if err == nil && cand != nil {
			resp["candidate"] = map[string]any{
				"candidate_id": cand.CandidateID,
				"score":        cand.Score,
				"offered_at":   cand.OfferedAt,
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func requestResponse(r *match.Request) map[string]any {
	resp := map[string]any{
		"request_id": r.ID, "state": r.State, "created_at": r.CreatedAt,
	}
	if !r.ResolvedAt.IsZero() {
		resp["resolved_at"] = r.ResolvedAt
	}
	if r.MatchedWith != "" {
		resp["matched_with"] = r.MatchedWith
	}
	return resp
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, match.ErrNotMatchable):
		writeError(w, http.StatusForbidden, "NOT_MATCHABLE", "You must opt in to matching in your privacy settings first.")
	case errors.Is(err, match.ErrNotAgeAssured):
		writeError(w, http.StatusForbidden, "AGE_ASSURANCE_REQUIRED", "Age assurance is required before matching.")
	case errors.Is(err, match.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many match requests — try again later.")
	case errors.Is(err, match.ErrRequestNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Match request not found.")
	case errors.Is(err, match.ErrNotOwner):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "This match request does not belong to you.")
	case errors.Is(err, match.ErrRequestNotSearching):
		writeError(w, http.StatusConflict, "NOT_SEARCHING", "This match request is no longer searching.")
	case errors.Is(err, match.ErrCandidateNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "No pending candidate with that id.")
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
