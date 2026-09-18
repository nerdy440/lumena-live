// Package handlers implements the HTTP endpoints for attachment upload and
// download (doc 07 §8's POST /attachments, roadmap Phase 10).
//
// Real presigned-URL upload means the client PUTs bytes directly to cloud
// storage, bypassing the app server. This dev build has no cloud storage,
// so the "presigned URL" this returns is just this server's own
// /attachments/{id}/upload endpoint — same contract shape (create a slot,
// then PUT bytes to a URL), no real S3 behind it. Download similarly
// requires only authentication + a clean scan result, not conversation
// participancy — a real deployment would check that the caller is a
// participant in the message referencing this attachment; that check isn't
// wired here since an attachment has no conversation association until
// after it's attached to a sent message.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/lumena/chat/attachsvc"
	"github.com/lumena/shared/ctxkeys"
)

func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

type Handler struct {
	svc *attachsvc.Service
}

func New(svc *attachsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── POST /attachments {content_type, filename} ─────────────────────────────

func (h *Handler) CreateUploadSlot(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	var req struct {
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	a := h.svc.CreateUploadSlot(r.Context(), accountID, req.ContentType, req.Filename)
	writeJSON(w, http.StatusCreated, map[string]any{
		"attachment_id": a.ID,
		"upload_url":    "/api/v1/attachments/" + a.ID + "/upload",
		"status":        a.Status,
	})
}

// ─── PUT /attachments/{id}/upload ────────────────────────────────────────────
// Accepts raw bytes and scans them. Not deliverable until this completes.

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	id := r.PathValue("id")
	data, err := io.ReadAll(io.LimitReader(r.Body, attachsvc.MaxAttachmentBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Could not read upload body.")
		return
	}
	a, err := h.svc.Upload(r.Context(), accountID, id, data)
	if err != nil {
		h.writeAttachError(w, err)
		return
	}
	resp := map[string]any{"attachment_id": a.ID, "status": a.Status}
	if a.Status == attachsvc.StatusBlocked {
		resp["block_reason"] = a.BlockReason
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── GET /attachments/{id} — metadata only ───────────────────────────────────

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeAttachError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attachment_id": a.ID, "status": a.Status, "content_type": a.ContentType,
		"filename": a.Filename, "size": a.Size,
	})
}

// ─── GET /attachments/{id}/download ──────────────────────────────────────────
// Only ever serves bytes for a clean attachment (doc 07 §8: "scanned
// before deliverable").

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Download(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeAttachError(w, err)
		return
	}
	w.Header().Set("Content-Type", a.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.Data)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (h *Handler) writeAttachError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, attachsvc.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Attachment not found.")
	case errors.Is(err, attachsvc.ErrNotOwner):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "This attachment does not belong to you.")
	case errors.Is(err, attachsvc.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "Attachment exceeds the size limit.")
	case errors.Is(err, attachsvc.ErrEmpty):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Attachment is empty.")
	case errors.Is(err, attachsvc.ErrAlreadyUploaded):
		writeError(w, http.StatusConflict, "ALREADY_UPLOADED", "This attachment has already been uploaded.")
	case errors.Is(err, attachsvc.ErrNotDeliverable):
		writeError(w, http.StatusUnprocessableEntity, "NOT_DELIVERABLE", "This attachment is still scanning or was blocked.")
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
