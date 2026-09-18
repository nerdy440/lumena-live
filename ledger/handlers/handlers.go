// Package handlers implements the HTTP endpoints for gifts and the wallet
// (doc 12 Phase 8/9): catalogue, send (Idempotency-Key required, doc 11
// §4), balance, and transaction history.
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/giftsvc"
	"github.com/lumena/ledger/ordersvc"
	"github.com/lumena/shared/ctxkeys"
)

func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	gifts  *giftsvc.Service
	ledger ledger.Repo
	orders *ordersvc.Service
	limits ledger.SpendLimitsRepo
}

func New(gifts *giftsvc.Service, ledgerRepo ledger.Repo) *Handler {
	return &Handler{gifts: gifts, ledger: ledgerRepo}
}

// WithOrders wires the Phase 9 order/purchase and spend-limit endpoints.
func (h *Handler) WithOrders(orders *ordersvc.Service, limits ledger.SpendLimitsRepo) *Handler {
	h.orders = orders
	h.limits = limits
	return h
}

// ─── GET /gifts/catalogue ───────────────────────────────────────────────────

func (h *Handler) Catalogue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": h.gifts.Catalogue()})
}

// ─── POST /gifts/send ────────────────────────────────────────────────────────
// Requires an Idempotency-Key header (architecture constraint: gift sends
// require an Idempotency-Key). The client generates it before the first
// attempt and reuses it on retry (doc 11 §4).

func (h *Handler) SendGift(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "An Idempotency-Key header is required for gift sends.")
		return
	}
	var req struct {
		RecipientID string `json:"recipient_id"`
		RoomID      string `json:"room_id"`
		GiftID      string `json:"gift_id"`
		Quantity    int64  `json:"quantity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	if req.Quantity == 0 {
		req.Quantity = 1
	}

	result, err := h.gifts.SendGift(r.Context(), accountID, req.RecipientID, req.RoomID, req.GiftID, req.Quantity, idempotencyKey)
	if err != nil {
		h.writeGiftError(w, err)
		return
	}
	status := http.StatusCreated
	if !result.IsNew {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"transaction_id":    result.TransactionID,
		"gift_id":           result.GiftID,
		"quantity":          result.Quantity,
		"coins_spent":       result.CoinsSpent,
		"creator_diamonds":  result.CreatorDiamonds,
		"platform_took":     result.PlatformTook,
		"new_balance":       result.NewSenderBalance,
	})
}

// ─── GET /wallet/balance ─────────────────────────────────────────────────────

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	coins, diamonds, err := h.gifts.Balance(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load balance.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"coins": coins, "diamonds": diamonds})
}

// ─── GET /wallet/transactions?cursor= ───────────────────────────────────────
// The user-visible transaction ledger (doc 12 Phase 9, BT-07).

func (h *Handler) TransactionHistory(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)

	coinItems, coinNext, err := h.ledger.History(r.Context(), ledger.UserCoinsAccount(accountID), cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load transaction history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": coinItems, "next_cursor": coinNext})
}

// ─── POST /wallet/dev-topup ──────────────────────────────────────────────────
// DEV-ONLY credit so gifts can be exercised before Phase 9's real IAP
// purchase flow exists — same pattern as streaming's DevIngestService and
// translate's DevTranslationProvider. NEVER ships to production: it credits
// coins with no payment behind them.

func (h *Handler) DevTopUp(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		Coins int64 `json:"coins"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Coins <= 0 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "coins must be a positive integer.")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		// No key supplied means the caller isn't asking for retry-safety
		// (this is a dev convenience button, not a real purchase) — give
		// each call its own unique key so repeat top-ups aren't silently
		// deduped against each other.
		idempotencyKey = "devtopup-" + accountID + "-" + randomHex(12)
	}
	_, _, err := h.ledger.PostTransaction(r.Context(), "promo", idempotencyKey, map[string]any{"reason": "dev_topup"}, []ledger.Entry{
		{AccountID: ledger.UserCoinsAccount(accountID), Amount: req.Coins, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -req.Coins, Currency: ledger.Coin},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not credit coins.")
		return
	}
	coins, diamonds, _ := h.gifts.Balance(r.Context(), accountID)
	writeJSON(w, http.StatusOK, map[string]any{"coins": coins, "diamonds": diamonds})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (h *Handler) writeGiftError(w http.ResponseWriter, err error) {
	var insufficient *ledger.InsufficientBalanceError
	switch {
	case errors.As(err, &insufficient):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": "INSUFFICIENT_BALANCE", "message": "Not enough coins for this gift.",
			"shortfall": insufficient.Shortfall(),
		}})
	case errors.Is(err, giftsvc.ErrGiftNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Unknown gift id.")
	case errors.Is(err, giftsvc.ErrSelfGift):
		writeError(w, http.StatusBadRequest, "SELF_GIFT", "You cannot send a gift to yourself.")
	case errors.Is(err, giftsvc.ErrInvalidQuantity):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Quantity must be positive and no more than the maximum allowed per send.")
	case errors.Is(err, giftsvc.ErrVelocityLimitExceeded):
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", err.Error())
	case errors.Is(err, giftsvc.ErrRecipientNotRoomHost):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "recipient_id is not the host of room_id.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not send gift.")
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

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
