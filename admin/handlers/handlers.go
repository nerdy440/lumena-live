// Package handlers implements the HTTP endpoints for the admin console
// (roadmap Phase 16) and user-facing support tickets (doc 07 §11, BT-08).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/lumena/admin"
	"github.com/lumena/admin/adminsvc"
	"github.com/lumena/shared/ctxkeys"
)

func accountID(r *http.Request) string {
	v, _ := r.Context().Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *adminsvc.Service
}

func New(svc *adminsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── Support tickets — user-facing (doc 07 §11) ─────────────────────────────

func (h *Handler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject  string `json:"subject"`
		Category string `json:"category"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Subject == "" || req.Body == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "subject and body are required.")
		return
	}
	t, err := h.svc.CreateTicket(r.Context(), accountID(r), req.Subject, req.Category, req.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not create ticket.")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) ListMyTickets(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListMyTickets(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load tickets.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) GetTicket(w http.ResponseWriter, r *http.Request) {
	t, msgs, err := h.svc.GetTicket(r.Context(), accountID(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": t, "messages": msgs})
}

func (h *Handler) ReplyTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "body is required.")
		return
	}
	msg, err := h.svc.ReplyTicket(r.Context(), accountID(r), r.PathValue("id"), req.Body)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// ─── Admin console (RBAC-gated) ─────────────────────────────────────────────

func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetAccount(r.Context(), accountID(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) RestrictAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.svc.RestrictAccount(r.Context(), accountID(r), r.PathValue("id"), req.Reason); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) UnsuspendAccount(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.UnsuspendAccount(r.Context(), accountID(r), r.PathValue("id")); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) ListEconomyAnomalies(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListEconomyAnomalies(r.Context(), accountID(r), 24*time.Hour)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) ListFraudQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListFraudQueue(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) ListTicketQueue(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListTicketQueue(r.Context(), accountID(r), admin.TicketStatus(r.URL.Query().Get("status")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) ResolveTicket(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ResolveTicket(r.Context(), accountID(r), r.PathValue("id")); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) GetPlatformHealth(w http.ResponseWriter, r *http.Request) {
	health, err := h.svc.GetPlatformHealth(r.Context(), accountID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, health)
}

// ─── Withdrawal management (finance role) ───────────────────────────────────

func (h *Handler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListPayouts(r.Context(), accountID(r), r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) ApprovePayout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, err := h.svc.ApprovePayout(r.Context(), accountID(r), r.PathValue("id"), req.Note)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) RejectPayout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reason is required.")
		return
	}
	p, err := h.svc.RejectPayout(r.Context(), accountID(r), r.PathValue("id"), req.Reason)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) MarkPayoutProcessing(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.MarkPayoutProcessing(r.Context(), accountID(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) MarkPayoutPaid(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PayoutReference string `json:"payout_reference"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PayoutReference == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "payout_reference is required.")
		return
	}
	p, err := h.svc.MarkPayoutPaid(r.Context(), accountID(r), r.PathValue("id"), req.PayoutReference)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) MarkPayoutFailed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reason is required.")
		return
	}
	p, err := h.svc.MarkPayoutFailed(r.Context(), accountID(r), r.PathValue("id"), req.Reason)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ─── Refunds / transaction reversal (finance role) ──────────────────────────

func (h *Handler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	tv, err := h.svc.GetLedgerTransaction(r.Context(), accountID(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

func (h *Handler) ReverseTransaction(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "An Idempotency-Key header is required to reverse a transaction.")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reason is required.")
		return
	}
	tv, err := h.svc.ReverseLedgerTransaction(r.Context(), accountID(r), r.PathValue("id"), idempotencyKey, req.Reason)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

func (h *Handler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListAuditLog(r.Context(), accountID(r), 100)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─── Dev-only role grant — see adminsvc.Service.DevGrantRole's doc
// comment. NEVER ships to production.

func (h *Handler) DevGrantRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "role is required.")
		return
	}
	if err := h.svc.DevGrantRole(r.Context(), accountID(r), admin.Role(req.Role)); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) GetMyRole(w http.ResponseWriter, r *http.Request) {
	role, ok, err := h.svc.GetRole(r.Context(), accountID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load role.")
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"role": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": role})
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrForbidden):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Your admin role does not permit this action.")
	case errors.Is(err, admin.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, "INVALID_ROLE", err.Error())
	case errors.Is(err, admin.ErrTicketNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Ticket not found.")
	case errors.Is(err, admin.ErrNotTicketParty):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Not a party to this ticket.")
	case errors.Is(err, admin.ErrPayoutNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Payout not found.")
	case errors.Is(err, admin.ErrInvalidPayoutTransition):
		writeError(w, http.StatusConflict, "INVALID_TRANSITION", "This payout is not in a state that allows this action.")
	case errors.Is(err, admin.ErrTransactionNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Transaction not found.")
	case errors.Is(err, admin.ErrAlreadyReversed):
		writeError(w, http.StatusConflict, "ALREADY_REVERSED", "This transaction has already been reversed.")
	case errors.Is(err, admin.ErrCannotReverse):
		writeError(w, http.StatusConflict, "CANNOT_REVERSE", "This transaction cannot be reversed.")
	case errors.Is(err, admin.ErrReversalWouldOverdraw):
		writeError(w, http.StatusConflict, "WOULD_OVERDRAW", "Reversing this transaction would overdraw an account — the funds have already been spent elsewhere.")
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
