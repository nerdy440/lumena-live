package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/store"
)

// ─── GET /products ──────────────────────────────────────────────────────────

func (h *Handler) Products(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": ledger.Products})
}

// ─── POST /orders {sku} — Idempotent ────────────────────────────────────────

func (h *Handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "An Idempotency-Key header is required for order creation.")
		return
	}
	var req struct {
		SKU string `json:"sku"`
		// Platform tells the server which store this purchase will go
		// through, so VerifyOrder later routes to the matching Verifier —
		// see ledger.Platform* constants. Empty defaults to the dev store.
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SKU == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "sku is required.")
		return
	}
	order, err := h.orders.CreateOrder(r.Context(), accountID, req.SKU, req.Platform, idempotencyKey)
	if err != nil {
		h.writeOrderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order_id": order.ID, "status": order.Status})
}

// ─── POST /orders/{id}/verify {purchase_token} — Idempotent ────────────────
// The only path that credits coins (doc 07 §6). Never silent while
// pending: a pending order is returned with 200 and its current status.

func (h *Handler) VerifyOrder(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	orderID := r.PathValue("id")
	var req struct {
		PurchaseToken string `json:"purchase_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PurchaseToken == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "purchase_token is required.")
		return
	}
	order, err := h.orders.VerifyOrder(r.Context(), accountID, orderID, req.PurchaseToken)
	if err != nil {
		h.writeOrderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orderStatusResponse(order))
}

// ─── GET /orders?status= ─────────────────────────────────────────────────────

func (h *Handler) ListOrders(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	status := ledger.OrderStatus(r.URL.Query().Get("status"))
	orders, err := h.orders.ListOrders(r.Context(), accountID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not list orders.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orders})
}

// ─── GET /orders/{id} ────────────────────────────────────────────────────────

func (h *Handler) GetOrder(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	order, err := h.orders.GetOrder(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		h.writeOrderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orderStatusResponse(order))
}

// ─── POST /orders/{id}/dispute ───────────────────────────────────────────────

func (h *Handler) DisputeOrder(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	caseID, err := h.orders.Dispute(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		h.writeOrderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"case_id": caseID})
}

// ─── GET/PATCH /me/spend-limits ──────────────────────────────────────────────

func (h *Handler) GetSpendLimits(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	limits, err := h.limits.Get(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load spend limits.")
		return
	}
	limits = ledger.ResolvePending(limits, time.Now())
	writeJSON(w, http.StatusOK, limits)
}

func (h *Handler) UpdateSpendLimits(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		DailyCap   int64 `json:"daily_cap"`
		WeeklyCap  int64 `json:"weekly_cap"`
		MonthlyCap int64 `json:"monthly_cap"`
		CoolingOff *bool `json:"cooling_off"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	current, err := h.limits.Get(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load spend limits.")
		return
	}
	updated := ledger.ApplyCapChange(current, req.DailyCap, req.WeeklyCap, req.MonthlyCap, time.Now())
	if req.CoolingOff != nil {
		updated.CoolingOff = *req.CoolingOff
		if *req.CoolingOff {
			updated.CoolingOffUntil = time.Now().Add(24 * time.Hour) // doc 11 §9: user-activated cooling-off
		} else {
			updated.CoolingOffUntil = time.Time{}
		}
	}
	if err := h.limits.Set(r.Context(), updated); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not save spend limits.")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func orderStatusResponse(order *ledger.Order) map[string]any {
	return map[string]any{
		"order_id": order.ID, "status": order.Status, "message": order.StatusReason,
		"coins": order.Coins, "sku": order.SKU,
	}
}

func (h *Handler) writeOrderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrUnknownSKU):
		writeError(w, http.StatusBadRequest, "UNKNOWN_SKU", "Unknown product sku.")
	case errors.Is(err, ledger.ErrOrderNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Order not found.")
	case errors.Is(err, ledger.ErrOrderNotOwned):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "This order does not belong to you.")
	case errors.Is(err, ledger.ErrOrderTerminal):
		writeError(w, http.StatusConflict, "ORDER_TERMINAL", "This order is already in a terminal state.")
	case errors.Is(err, ledger.ErrTokenAlreadyUsed):
		writeError(w, http.StatusConflict, "TOKEN_ALREADY_USED", "This purchase token has already been used.")
	case errors.Is(err, store.ErrInvalidToken):
		writeError(w, http.StatusBadRequest, "INVALID_TOKEN", "The purchase token could not be verified.")
	case errors.Is(err, ledger.ErrCoolingOff):
		writeError(w, http.StatusForbidden, "COOLING_OFF", "Purchases are blocked during your cooling-off period.")
	case errors.Is(err, ledger.ErrSpendLimitExceeded):
		writeError(w, http.StatusForbidden, "SPEND_LIMIT_EXCEEDED", "This purchase would exceed a spend limit you set.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Something went wrong.")
	}
}
