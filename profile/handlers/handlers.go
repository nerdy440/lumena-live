// Package handlers implements the HTTP layer for profile and social graph endpoints.
// All endpoints from doc 07 §3.
package handlers

import (
	"context"

	"github.com/lumena/shared/ctxkeys"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lumena/profile"
	"github.com/lumena/profile/profilesvc"
	"github.com/lumena/profile/social"
)




func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

func WithAccountID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxkeys.AccountID, id)
}

// Handler holds all profile+social HTTP handlers.
type Handler struct {
	svc *profilesvc.Service
}

func New(svc *profilesvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── Profile ──────────────────────────────────────────────────────────────────

// GET /users/{id}
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	subjectID := r.PathValue("id")
	if subjectID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "User ID is required.")
		return
	}

	viewerID := AccountIDFromCtx(r.Context())

	view, err := h.svc.GetProfile(r.Context(), viewerID, subjectID)
	if err != nil {
		if errors.Is(err, profilesvc.ErrProfileUnavailable) {
			// Same 404 for not-found, blocked, and suspended (doc 04 §3)
			writeError(w, http.StatusNotFound, "NOT_FOUND", "This profile isn't available.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load profile.")
		return
	}

	writeJSON(w, http.StatusOK, toProfileResponse(view))
}

// GET /me
func (h *Handler) GetMyProfile(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	view, err := h.svc.GetProfile(r.Context(), accountID, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load profile.")
		return
	}
	writeJSON(w, http.StatusOK, toProfileResponse(view))
}

// PATCH /me
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}

	var body struct {
		DisplayName *string  `json:"display_name"`
		Handle      *string  `json:"handle"`
		Bio         *string  `json:"bio"`
		AvatarURL   *string  `json:"avatar_url"`
		Languages   []string `json:"languages"`
		Interests   []string `json:"interests"`
		RegionCode  *string  `json:"region_code"`
	}
	if !decode(w, r, &body) {
		return
	}

	req := profile.UpdateRequest{
		DisplayName: body.DisplayName,
		Handle:      body.Handle,
		Bio:         body.Bio,
		AvatarURL:   body.AvatarURL,
		Languages:   body.Languages,
		Interests:   body.Interests,
		RegionCode:  body.RegionCode,
	}

	p, err := h.svc.UpdateProfile(r.Context(), accountID, req)
	if err != nil {
		if errors.Is(err, profile.ErrHandleTaken) {
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", "That handle is already taken.")
			return
		}
		if errors.Is(err, profile.ErrInvalidHandle) {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", profile.ErrInvalidHandle.Error())
			return
		}
		if errors.Is(err, profile.ErrDisplayNameLen) {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", profile.ErrDisplayNameLen.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not update profile.")
		return
	}

	writeJSON(w, http.StatusOK, toProfileDTO(p))
}

// ─── Social graph ─────────────────────────────────────────────────────────────

// GET /users/{id}/relationship
func (h *Handler) GetRelationship(w http.ResponseWriter, r *http.Request) {
	viewerID := AccountIDFromCtx(r.Context())
	if viewerID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	subjectID := r.PathValue("id")

	rel, err := h.svc.GetRelationship(r.Context(), viewerID, subjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load relationship.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relationship": rel})
}

// POST /follows
func (h *Handler) Follow(w http.ResponseWriter, r *http.Request) {
	followerID := AccountIDFromCtx(r.Context())
	if followerID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}

	var body struct {
		FolloweeID string `json:"followee_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.FolloweeID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "followee_id is required.")
		return
	}

	rel, err := h.svc.Follow(r.Context(), followerID, body.FolloweeID)
	if err != nil {
		if errors.Is(err, social.ErrBlockedByOp) {
			writeError(w, http.StatusForbidden, "RECIPIENT_UNAVAILABLE", "Cannot follow this user.")
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}

	// Returns the authoritative relationship — client renders from this, not from a guess.
	writeJSON(w, http.StatusOK, map[string]any{"relationship": rel})
}

// DELETE /follows/{followee_id}
// This is the first-class unfollow (BT-02) — never requires blocking.
func (h *Handler) Unfollow(w http.ResponseWriter, r *http.Request) {
	followerID := AccountIDFromCtx(r.Context())
	if followerID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	followeeID := r.PathValue("followee_id")
	if followeeID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "followee_id is required.")
		return
	}

	rel, err := h.svc.Unfollow(r.Context(), followerID, followeeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}

	// Returns the authoritative relationship — client renders from this.
	writeJSON(w, http.StatusOK, map[string]any{"relationship": rel})
}

// GET /users/{id}/following
func (h *Handler) GetFollowing(w http.ResponseWriter, r *http.Request) {
	requesterID := AccountIDFromCtx(r.Context())
	subjectID := r.PathValue("id")
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)

	entries, nextCursor, err := h.svc.GetFollowing(r.Context(), subjectID, requesterID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load following list.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       entries,
		"next_cursor": nextCursor,
	})
}

// GET /users/{id}/followers
func (h *Handler) GetFollowers(w http.ResponseWriter, r *http.Request) {
	subjectID := r.PathValue("id")
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)

	entries, nextCursor, err := h.svc.GetFollowers(r.Context(), subjectID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load followers list.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       entries,
		"next_cursor": nextCursor,
	})
}

// GET /me/blocked
func (h *Handler) GetBlocked(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)

	entries, nextCursor, err := h.svc.GetBlocked(r.Context(), accountID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load blocked accounts.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       entries,
		"next_cursor": nextCursor,
	})
}

// POST /blocks
func (h *Handler) Block(w http.ResponseWriter, r *http.Request) {
	blockerID := AccountIDFromCtx(r.Context())
	if blockerID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	var body struct {
		BlockedID string `json:"blocked_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	rel, err := h.svc.Block(r.Context(), blockerID, body.BlockedID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relationship": rel})
}

// DELETE /blocks/{id}
func (h *Handler) Unblock(w http.ResponseWriter, r *http.Request) {
	blockerID := AccountIDFromCtx(r.Context())
	if blockerID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	blockedID := r.PathValue("id")
	rel, err := h.svc.Unblock(r.Context(), blockerID, blockedID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not unblock.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relationship": rel})
}

// GET /me/privacy  PATCH /me/privacy
func (h *Handler) GetPrivacy(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	settings, err := h.svc.GetPrivacySettings(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load privacy settings.")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) UpdatePrivacy(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	var settings profile.PrivacySettings
	if !decode(w, r, &settings) {
		return
	}
	updated, err := h.svc.UpdatePrivacySettings(r.Context(), accountID, settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not update privacy settings.")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ─── Response DTOs ────────────────────────────────────────────────────────────

type profileDTO struct {
	AccountID   string   `json:"account_id"`
	DisplayName string   `json:"display_name"`
	Handle      *string  `json:"handle"`
	Bio         *string  `json:"bio"`
	AvatarURL   *string  `json:"avatar_url"`
	AvatarState string   `json:"avatar_state"`
	Languages   []string `json:"languages"`
	Interests   []string `json:"interests"`
	RegionCode  string   `json:"region_code"`
	Level       int      `json:"level"`
	IsCreator   bool     `json:"is_creator"`
}

type profileResponse struct {
	Profile      profileDTO              `json:"profile"`
	Counts       *profile.ProfileCounts  `json:"counts,omitempty"`
	Relationship *social.Relationship    `json:"relationship,omitempty"`
	IsOwner      bool                    `json:"is_owner"`
}

func toProfileDTO(p *profile.Profile) profileDTO {
	langs := p.Languages
	if langs == nil {
		langs = []string{}
	}
	interests := p.Interests
	if interests == nil {
		interests = []string{}
	}
	return profileDTO{
		AccountID:   p.AccountID,
		DisplayName: p.DisplayName,
		Handle:      p.Handle,
		Bio:         p.Bio,
		AvatarURL:   p.AvatarURL,
		AvatarState: string(p.AvatarState),
		Languages:   langs,
		Interests:   interests,
		RegionCode:  p.RegionCode,
		Level:       p.Level,
		IsCreator:   p.IsCreator,
	}
}

func toProfileResponse(view *profilesvc.ProfileView) profileResponse {
	return profileResponse{
		Profile:      toProfileDTO(view.Profile),
		Counts:       view.Counts,
		Relationship: view.Relationship,
		IsOwner:      view.IsOwner,
	}
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Request body is malformed JSON.")
		return false
	}
	return true
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

// Suppress unused import warning
var _ = strings.TrimSpace
