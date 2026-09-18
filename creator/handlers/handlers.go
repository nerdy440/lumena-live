// Package handlers implements the HTTP endpoints for the creator dashboard
// (doc 03 CRTR_001-3, roadmap Phase 14).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/creator"
	"github.com/lumena/creator/creatorsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *creatorsvc.Service
}

func New(svc *creatorsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── GET /me/creator/kyc ──────────────────────────────────────────────────

func (h *Handler) GetKYC(w http.ResponseWriter, r *http.Request) {
	rec, err := h.svc.GetKYCStatus(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load KYC status.")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ─── POST /me/creator/kyc {legal_name, country} ───────────────────────────

func (h *Handler) SubmitKYC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LegalName string `json:"legal_name"`
		Country   string `json:"country"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LegalName == "" || req.Country == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "legal_name and country are required.")
		return
	}
	rec, err := h.svc.SubmitKYC(r.Context(), accountID(r), req.LegalName, req.Country)
	if err != nil {
		if errors.Is(err, creator.ErrKYCAlreadyVerified) {
			writeError(w, http.StatusConflict, "ALREADY_VERIFIED", "KYC is already verified.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not submit KYC.")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ─── POST /me/creator/kyc/dev-approve ─────────────────────────────────────
// Dev-only escalation standing in for the real KYC vendor. See
// creatorsvc.Service.DevApproveKYC's doc comment. NEVER ships to production.

func (h *Handler) DevApproveKYC(w http.ResponseWriter, r *http.Request) {
	rec, err := h.svc.DevApproveKYC(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not approve KYC.")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ─── GET /me/creator/earnings ──────────────────────────────────────────────

func (h *Handler) GetEarnings(w http.ResponseWriter, r *http.Request) {
	summary, err := h.svc.GetEarningsSummary(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load earnings.")
		return
	}
	entries := make([]map[string]any, 0, len(summary.Entries))
	for _, e := range summary.Entries {
		item := map[string]any{
			"transaction_id":  e.TransactionID,
			"kind":            e.Kind,
			"amount_diamonds": e.AmountDiamonds,
			"created_at":      e.CreatedAt,
		}
		if e.GiftID != "" {
			item["gift_id"] = e.GiftID
		}
		entries = append(entries, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_earned_diamonds":    summary.TotalEarnedDiamonds,
		"locked_diamonds":          summary.LockedDiamonds,
		"available_diamonds":       summary.AvailableDiamonds,
		"paid_out_diamonds":        summary.PaidOutDiamonds,
		"current_balance_diamonds": summary.CurrentBalance,
		"gift_breakdown":           summary.ByGift,
		"entries":                  entries,
	})
}

// ─── GET /me/creator/analytics ─────────────────────────────────────────────

func (h *Handler) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.GetAnalytics(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load analytics.")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// ─── POST /me/creator/payouts {amount_diamonds}, GET /me/creator/payouts ──

func (h *Handler) RequestPayout(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required.")
		return
	}
	var req struct {
		AmountDiamonds int64 `json:"amount_diamonds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	p, err := h.svc.RequestPayout(r.Context(), accountID(r), req.AmountDiamonds, idempotencyKey)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListPayouts(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load payouts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─── GET /payout-countries ──────────────────────────────────────────────────
// Public (no auth required) — the creator payout-profile form needs this
// list before a creator has necessarily done anything else, and it carries
// no sensitive data (doc rule 25's supported_payout_countries table).

func (h *Handler) ListPayoutCountries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": creator.PayoutCountries})
}

// ─── GET/PUT /me/creator/payout-profile ────────────────────────────────────
// Deliberately minimal: method + country + payee name + one destination
// identifier (bank account number / mobile wallet number / PayPal email).
// Never a bank login, card CVV, or payment-provider secret (doc rules
// 20/21) — this is only enough for an admin to know where to manually send
// a payout during the MVP's manual payout process.

func (h *Handler) GetPayoutProfile(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPayoutProfile(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load payout profile.")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) SetPayoutProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method      string `json:"method"`
		Country     string `json:"country"`
		PayeeName   string `json:"payee_name"`
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	method := creator.PayoutMethod(req.Method)
	switch method {
	case creator.PayoutMethodBankTransfer, creator.PayoutMethodMobileWallet, creator.PayoutMethodPayPal:
	default:
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "method must be one of bank_transfer, mobile_wallet, paypal.")
		return
	}
	if req.Country == "" || req.PayeeName == "" || req.Destination == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "country, payee_name and destination are required.")
		return
	}
	p, err := h.svc.SetPayoutProfile(r.Context(), accountID(r), method, req.Country, req.PayeeName, req.Destination)
	if err != nil {
		switch {
		case errors.Is(err, creator.ErrUnsupportedPayoutCountry):
			writeError(w, http.StatusBadRequest, "UNSUPPORTED_COUNTRY", "Withdrawals are not yet supported for this country.")
		case errors.Is(err, creator.ErrUnsupportedPayoutMethod):
			writeError(w, http.StatusBadRequest, "UNSUPPORTED_METHOD", "This payout method is not available for the selected country.")
		default:
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not save payout profile.")
		}
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, creator.ErrKYCIncomplete):
		writeError(w, http.StatusForbidden, "KYC_REQUIRED", "KYC must be verified before requesting a payout.")
	case errors.Is(err, creator.ErrPayoutProfileRequired):
		writeError(w, http.StatusForbidden, "PAYOUT_PROFILE_REQUIRED", "Set a payout profile before requesting a withdrawal.")
	case errors.Is(err, creator.ErrAccountRestricted):
		writeError(w, http.StatusForbidden, "ACCOUNT_RESTRICTED", "Payouts are unavailable while your account is restricted.")
	case errors.Is(err, creator.ErrBelowThreshold):
		writeError(w, http.StatusForbidden, "BELOW_THRESHOLD", "Available balance is below the minimum payout threshold.")
	case errors.Is(err, creator.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "INVALID_AMOUNT", "Payout amount must be positive and no more than your available balance.")
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
