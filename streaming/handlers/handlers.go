// Package handlers implements the streaming HTTP handlers.
// Wires into the unified API server alongside auth/profile/feed handlers.
package handlers

import (
	"context"

	"github.com/lumena/shared/ctxkeys"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumena/streaming"
)

func accountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

// Handler holds the streaming HTTP handlers.
type Handler struct {
	svc *streaming.BroadcastService
}

func New(svc *streaming.BroadcastService) *Handler {
	return &Handler{svc: svc}
}

// POST /api/v1/streams
// Creates a live room session and issues ingest credentials.
// Gate: requires auth (enforced by sharedAuth middleware in main.go).
// Age assurance gate: NOT IMPLEMENTED until Phase 0 vendor integration.
// When implemented: check account_age.status = 'assured' before proceeding.
func (h *Handler) StartBroadcast(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}

	var body struct {
		RoomID string `json:"room_id"`
		Region string `json:"region_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Malformed request body.")
		return
	}
	if body.RoomID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "room_id is required.")
		return
	}
	if body.Region == "" {
		body.Region = "us-east"
	}

	// Age assurance gate (NOT IMPLEMENTED — Phase 0).
	// Uncomment when age assurance vendor is integrated:
	// if ageStatus != "assured" {
	//     writeError(w, http.StatusForbidden, "AGE_ASSURANCE_REQUIRED",
	//         "Age verification is required before broadcasting.")
	//     return
	// }

	sess, creds, err := h.svc.StartBroadcast(r.Context(), body.RoomID, accountID, body.Region)
	if err != nil {
		if errors.Is(err, streaming.ErrAgeNotAssured) {
			writeError(w, http.StatusForbidden, "AGE_ASSURANCE_REQUIRED",
				"Age verification is required before broadcasting.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not start broadcast.")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id":  sess.ID,
		"room_id":     sess.RoomID,
		"state":       sess.State,
		"playback_url": sess.PlaybackURL,
		"ingest_url":  creds.IngestURL,
		"stream_key":  creds.StreamKey, // returned ONCE; never stored
		"expires_at":  creds.ExpiresAt,
		"ladder":      streaming.DefaultLadder(),
		// Note to client SDK: stream_key must never be logged or cached.
	})
}

// POST /api/v1/streams/{id}/stop
func (h *Handler) EndBroadcast(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Session ID required.")
		return
	}

	if err := h.svc.EndBroadcast(r.Context(), sessionID, accountID); err != nil {
		if errors.Is(err, streaming.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Stream session not found.")
			return
		}
		writeError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/streams/{id}
// Returns session info for a room's active stream (used by viewer on room join).
func (h *Handler) GetStream(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Room ID required.")
		return
	}
	sess, err := h.svc.GetSession(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, streaming.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "No active stream for this room.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load stream.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  sess.ID,
		"room_id":     sess.RoomID,
		"state":       sess.State,
		"playback_url": sess.PlaybackURL,
		"health":      sess.Health,
		// stream_key is NEVER returned to viewers — only issued to the broadcaster once
	})
}

// GET /api/v1/streams/{id}/health
// Returns real-time health metrics — used by the broadcaster's quality indicator.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	sessionID := r.PathValue("id")
	health, err := h.svc.GetHealth(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, streaming.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Session not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not get health.")
		return
	}
	writeJSON(w, http.StatusOK, health)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

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
