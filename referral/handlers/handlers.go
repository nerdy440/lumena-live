// Package handlers implements the HTTP endpoints for the referral/invite
// system (see the referral package's doc comment for the design).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/referral"
	"github.com/lumena/referral/referralsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *referralsvc.Service
}

func New(svc *referralsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── POST /me/referrals/claim {code} ───────────────────────────────────────

func (h *Handler) ClaimCode(w http.ResponseWriter, r *http.Request) {
	accID := accountID(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "code is required.")
		return
	}
	rec, err := h.svc.ClaimCode(r.Context(), accID, req.Code)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ─── GET /me/referrals ──────────────────────────────────────────────────────

func (h *Handler) ListMyReferrals(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListMyReferrals(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load referrals.")
		return
	}
	var totalCoins int64
	for _, it := range items {
		totalCoins += it.RewardCoins
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total_coins_earned": totalCoins, "my_code": accountID(r),
	})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, referral.ErrSelfReferral):
		writeError(w, http.StatusBadRequest, "SELF_REFERRAL", "You cannot redeem your own referral code.")
	case errors.Is(err, referral.ErrCodeNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "No account matches this referral code.")
	case errors.Is(err, referral.ErrAlreadyClaimed):
		writeError(w, http.StatusConflict, "ALREADY_CLAIMED", "You've already redeemed a referral code.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not redeem this code.")
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
