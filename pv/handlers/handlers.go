// Package handlers implements HTTP handlers for private video calls.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/ledger"
	"github.com/lumena/pv"
	"github.com/lumena/pv/pvsvc"
	"github.com/lumena/shared/ctxkeys"
)

func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *pvsvc.Service
}

func New(svc *pvsvc.Service) *Handler {
	return &Handler{svc: svc}
}

func sessionJSON(s *pv.Session) map[string]any {
	out := map[string]any{
		"session_id":         s.ID,
		"caller_id":          s.CallerID,
		"callee_id":          s.CalleeID,
		"state":              string(s.State),
		"rate_coins_per_min": s.RatePerMinCoins,
		"consumed_coins":     s.ConsumedCoins,
		"created_at":         s.CreatedAt,
	}
	if s.AcceptedAt != nil {
		out["accepted_at"] = *s.AcceptedAt
	}
	if s.EndedAt != nil {
		out["ended_at"] = *s.EndedAt
	}
	return out
}

// RequestSession — POST /private-video/requests {callee_id, rate_coins_per_min}
func (h *Handler) RequestSession(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		CalleeID        string `json:"callee_id"`
		RatePerMinCoins int64  `json:"rate_coins_per_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CalleeID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "callee_id is required.")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "An Idempotency-Key header is required.")
		return
	}
	sess, _, err := h.svc.RequestSession(r.Context(), accountID, req.CalleeID, req.RatePerMinCoins, idempotencyKey)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(sess))
}

func (h *Handler) AcceptSession(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	sessionID := r.PathValue("id")
	sess, err := h.svc.AcceptSession(r.Context(), accountID, sessionID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(sess))
}

func (h *Handler) DeclineSession(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	sessionID := r.PathValue("id")
	sess, err := h.svc.DeclineSession(r.Context(), accountID, sessionID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(sess))
}

func (h *Handler) EndSession(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	sessionID := r.PathValue("id")
	sess, err := h.svc.EndSession(r.Context(), accountID, sessionID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(sess))
}

func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	sessionID := r.PathValue("id")
	sess, err := h.svc.GetSession(r.Context(), accountID, sessionID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(sess))
}

func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	sessions, err := h.svc.ListSessions(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load sessions.")
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for i := range sessions {
		items = append(items, sessionJSON(&sessions[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	var insufficient *ledger.InsufficientBalanceError
	switch {
	case errors.Is(err, pvsvc.ErrSelfCall):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "You can't call yourself.")
	case errors.Is(err, pvsvc.ErrAccountNotActive):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Your account can't start calls right now.")
	case errors.Is(err, pvsvc.ErrAgeNotAssured):
		writeError(w, http.StatusForbidden, "AGE_NOT_ASSURED", "Age verification is required for private video calls.")
	case errors.Is(err, pvsvc.ErrCalleeBlocked):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "You can't call this user.")
	case errors.Is(err, pvsvc.ErrCalleeNotReachable):
		writeError(w, http.StatusForbidden, "NOT_REACHABLE", "This user isn't accepting calls right now.")
	case errors.Is(err, pvsvc.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "This call session doesn't exist.")
	case errors.Is(err, pvsvc.ErrNotParticipant):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "You're not a participant in this call.")
	case errors.Is(err, pvsvc.ErrInvalidState):
		writeError(w, http.StatusConflict, "INVALID_STATE", "This call can't do that right now.")
	case errors.As(err, &insufficient):
		writeError(w, http.StatusPaymentRequired, "INSUFFICIENT_BALANCE", "Not enough coins to start this call.")
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
