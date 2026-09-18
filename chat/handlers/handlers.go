// Package handlers implements the HTTP endpoints for private chat from
// doc 07 §7: conversation list, message history, send, read receipts,
// and request accept/decline.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/lumena/chat"
	"github.com/lumena/chat/chatsvc"
	"github.com/lumena/shared/ctxkeys"
)

func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *chatsvc.Service
}

func New(svc *chatsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── GET /conversations?cursor=&folder=inbox|requests ─────────────────────────

func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "inbox"
	}
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 20)

	items, next, err := h.svc.ListConversations(r.Context(), accountID, folder, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load conversations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// ─── POST /conversations {other_id} ────────────────────────────────────────────
// Starts (or returns the existing) conversation with other_id.

func (h *Handler) StartConversation(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var body struct {
		OtherID string `json:"other_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OtherID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "other_id is required.")
		return
	}
	conv, err := h.svc.StartConversation(r.Context(), accountID, body.OtherID)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conv})
}

// ─── GET /conversations/{id}/messages?cursor= ──────────────────────────────────

func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	conversationID := r.PathValue("id")
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)

	msgs, next, err := h.svc.ListMessages(r.Context(), accountID, conversationID, cursor, limit)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": msgs, "next_cursor": next})
}

// ─── POST /conversations/{id}/messages {body, client_msg_id} ──────────────────
// Idempotent on client_msg_id (doc 07 §7).

func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	conversationID := r.PathValue("id")
	var req struct {
		Body         string `json:"body"`
		ClientMsgID  string `json:"client_msg_id"`
		AttachmentID string `json:"attachment_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ClientMsgID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "body and client_msg_id are required.")
		return
	}
	msg, isNew, err := h.svc.SendMessage(r.Context(), accountID, conversationID, req.ClientMsgID, req.Body, req.AttachmentID)
	if err != nil {
		writeChatError(w, err)
		return
	}
	status := http.StatusCreated
	if !isNew {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"message": msg})
}

// ─── POST /conversations/{id}/read {up_to_seq} ─────────────────────────────────

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	conversationID := r.PathValue("id")
	var req struct {
		UpToSeq uint64 `json:"up_to_seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "up_to_seq is required.")
		return
	}
	if err := h.svc.MarkRead(r.Context(), accountID, conversationID, req.UpToSeq); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── POST /conversations/{id}/accept and /decline ──────────────────────────────

func (h *Handler) AcceptRequest(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if err := h.svc.AcceptRequest(r.Context(), accountID, r.PathValue("id")); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) DeclineRequest(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if err := h.svc.DeclineRequest(r.Context(), accountID, r.PathValue("id")); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── DELETE /messages/{id}?scope=me|everyone ───────────────────────────────────

func (h *Handler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "me"
	}
	if err := h.svc.DeleteMessage(r.Context(), accountID, r.PathValue("id"), scope); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func writeChatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrSelfMessage):
		writeError(w, http.StatusBadRequest, "SELF_MESSAGE", "You cannot message yourself.")
	case errors.Is(err, chat.ErrBlocked):
		writeError(w, http.StatusForbidden, "BLOCKED", "This action is not allowed between blocked accounts.")
	case errors.Is(err, chat.ErrNotParticipant):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "You are not a participant in this conversation.")
	case errors.Is(err, chat.ErrConversationNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Conversation not found.")
	case errors.Is(err, chat.ErrMessageNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Message not found.")
	case errors.Is(err, chat.ErrEmptyBody):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Message body must not be empty.")
	case errors.Is(err, chat.ErrCannotAcceptOwnRequest):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "You cannot accept or decline your own outgoing request.")
	case errors.Is(err, chat.ErrAttachmentNotDeliverable):
		writeError(w, http.StatusBadRequest, "ATTACHMENT_NOT_DELIVERABLE", "This attachment is still scanning, blocked, or not yours.")
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
