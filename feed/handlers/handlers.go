// Package handlers implements HTTP handlers for feed, discovery, rooms, and notifications.
// All endpoints from doc 07 §4–5.
package handlers

import (
	"context"

	"github.com/lumena/shared/ctxkeys"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lumena/feed"
	"github.com/lumena/feed/feedsvc"
	"github.com/lumena/ledger"
)


func AccountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}
func WithAccountID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxkeys.AccountID, id)
}

// Handler holds all feed HTTP handlers.
type Handler struct {
	svc *feedsvc.Service
}

func New(svc *feedsvc.Service) *Handler {
	return &Handler{svc: svc}
}

// ─── GET /feed ────────────────────────────────────────────────────────────────
// Query params: tab=for_you|hot|explore|following, cursor=, limit=
// Guest-accessible: yes (with reduced personalization).

func (h *Handler) GetFeed(w http.ResponseWriter, r *http.Request) {
	tab := feed.Tab(r.URL.Query().Get("tab"))
	if tab == "" {
		tab = feed.TabForYou
	}
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 20)
	accountID := AccountIDFromCtx(r.Context())

	// Build user context from request.
	// In production this would be loaded from the profile service.
	// Phase 4: derive from query params + defaults.
	user := buildUserContext(r, accountID)

	page, err := h.svc.GetFeed(r.Context(), tab, user, cursor, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// ─── GET /rooms/{id} ─────────────────────────────────────────────────────────

func (h *Handler) GetRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Room ID is required.")
		return
	}
	room, err := h.svc.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, feedsvc.ErrRoomNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "This room doesn't exist or has ended.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load room.")
		return
	}
	card := room.ToCard()
	// IsUnlocked is viewer-specific (an anonymous/guest request never gets
	// true for a premium room), so it's computed here rather than in
	// ToCard(), which knows nothing about who's asking.
	unlocked, err := h.svc.IsRoomUnlockedForViewer(r.Context(), room, AccountIDFromCtx(r.Context()))
	if err == nil {
		card.IsUnlocked = unlocked
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": card})
}

// ─── POST /rooms/{id}/premium {is_premium, unlock_price_coins} ─────────────
// Host-only: turns the room's paywall on/off.

func (h *Handler) SetRoomPremium(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	roomID := r.PathValue("id")
	var req struct {
		IsPremium        bool  `json:"is_premium"`
		UnlockPriceCoins int64 `json:"unlock_price_coins"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body.")
		return
	}
	room, err := h.svc.SetRoomPremium(r.Context(), accountID, roomID, req.IsPremium, req.UnlockPriceCoins)
	if err != nil {
		h.writePremiumError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room.ToCard()})
}

// ─── POST /rooms/{id}/unlock — Idempotent ────────────────────────────────────
// Charges the viewer room.UnlockPriceCoins once; a retry with the same
// Idempotency-Key (or simply re-calling once already unlocked) never
// re-charges — same idempotency discipline as gift sends and orders.

func (h *Handler) UnlockRoom(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	roomID := r.PathValue("id")
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "An Idempotency-Key header is required.")
		return
	}
	room, err := h.svc.UnlockRoom(r.Context(), accountID, roomID, idempotencyKey)
	if err != nil {
		h.writePremiumError(w, err)
		return
	}
	card := room.ToCard()
	card.IsUnlocked = true
	writeJSON(w, http.StatusOK, map[string]any{"room": card})
}

func (h *Handler) writePremiumError(w http.ResponseWriter, err error) {
	var insufficient *ledger.InsufficientBalanceError
	switch {
	case errors.Is(err, feedsvc.ErrRoomNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "This room doesn't exist or has ended.")
	case errors.Is(err, feedsvc.ErrNotRoomHost):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only the room's host can do this.")
	case errors.Is(err, feedsvc.ErrInvalidUnlockPrice):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "A premium room needs a positive unlock price.")
	case errors.Is(err, feedsvc.ErrPremiumFeatureDisabled):
		writeError(w, http.StatusServiceUnavailable, "FEATURE_DISABLED", "Premium rooms are not enabled on this server.")
	case errors.As(err, &insufficient):
		writeError(w, http.StatusPaymentRequired, "INSUFFICIENT_BALANCE", "Not enough coins to unlock this room.")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Something went wrong.")
	}
}

// ─── POST /streams ────────────────────────────────────────────────────────────
// Creates a live room. Age-assurance gate enforced by middleware (NOT IMPLEMENTED yet).

func (h *Handler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}

	var body struct {
		Title    string   `json:"title"`
		Language string   `json:"language"`
		Region   string   `json:"region_code"`
		Tags     []string `json:"tags"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Title is required.")
		return
	}
	if body.Language == "" {
		body.Language = "en"
	}
	if body.Region == "" {
		body.Region = "XX"
	}

	// Age assurance gate: NOT IMPLEMENTED (Phase 0 / Phase 5).
	// In production: check account_age.status = 'assured' before proceeding.
	// writeError(w, http.StatusForbidden, "AGE_ASSURANCE_REQUIRED", "..."); return

	room, err := h.svc.CreateRoom(r.Context(), accountID, accountID, body.Title, body.Language, body.Region, body.Tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not create room.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"room": room.ToCard(),
		// In production: also return short-lived ingest credentials (Phase 5)
		"ingest_url": "NOT_IMPLEMENTED — Phase 5 (streaming architecture)",
		"stream_key": "NOT_IMPLEMENTED — Phase 5",
	})
}

// POST /streams/{id}/stop
func (h *Handler) EndRoom(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	roomID := r.PathValue("id")
	if err := h.svc.EndRoom(r.Context(), roomID, accountID, "host_ended"); err != nil {
		if errors.Is(err, feedsvc.ErrRoomNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Room not found.")
			return
		}
		writeError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /search ──────────────────────────────────────────────────────────────

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Search query is required.")
		return
	}
	typesRaw := r.URL.Query().Get("type")
	var types []string
	if typesRaw != "" {
		types = strings.Split(typesRaw, ",")
	}
	limit := queryInt(r, "limit", 20)

	results, err := h.svc.Search(r.Context(), q, types, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results, "query": q})
}

// ─── Notifications ────────────────────────────────────────────────────────────

func (h *Handler) GetNotifications(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	cursor := r.URL.Query().Get("cursor")
	limit := queryInt(r, "limit", 50)
	notifs, nextCursor, err := h.svc.GetNotifications(r.Context(), accountID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load notifications.")
		return
	}
	if notifs == nil {
		notifs = []feed.Notification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": notifs, "next_cursor": nextCursor})
}

func (h *Handler) GetNotificationSummary(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeJSON(w, http.StatusOK, feed.NotificationSummary{UnreadCount: 0})
		return
	}
	summary, err := h.svc.GetNotificationSummary(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) MarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.")
		return
	}
	var body struct {
		UpToID string `json:"up_to_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.svc.MarkNotificationsRead(r.Context(), accountID, body.UpToID); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func buildUserContext(r *http.Request, accountID string) feed.UserFeedContext {
	region := r.Header.Get("X-Region-Code")
	if region == "" {
		region = "US"
	}
	lang := acceptLanguage(r.Header.Get("Accept-Language"))
	// In production: load languages, interests, follows from the profile service.
	// Phase 4: use headers as approximation.
	return feed.UserFeedContext{
		AccountID:  accountID,
		Languages:  []string{lang},
		RegionCode: region,
	}
}

func acceptLanguage(header string) string {
	if header == "" {
		return "en"
	}
	// Take first language tag, strip quality value
	parts := strings.Split(header, ",")
	if len(parts) == 0 {
		return "en"
	}
	lang := strings.TrimSpace(strings.Split(parts[0], ";")[0])
	if lang == "" {
		return "en"
	}
	// Normalize: "en-US" → "en"
	return strings.ToLower(strings.Split(lang, "-")[0])
}

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
