// Package feedsvc is the application service for feed, discovery, and notifications.
package feedsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lumena/feed"
)

// Service orchestrates the feed, search, and notification domains.
// UnlockCoinsFunc posts a viewer's one-time premium-room payment via the
// real ledger (coins from the viewer, platform take, diamonds to the
// host — same economics as a gift send) and returns the transaction ID.
// Injected rather than importing ledger directly, same DI-via-closures
// pattern every other module in this workspace uses.
type UnlockCoinsFunc func(ctx context.Context, viewerID, hostID string, priceCoins int64, idempotencyKey string) (transactionID string, err error)

type Service struct {
	rooms       feed.RoomRepo
	notifs      feed.NotificationRepo
	engine      feed.Engine
	unlocks     feed.PremiumUnlockRepo
	unlockCoins UnlockCoinsFunc
}

func NewService(rooms feed.RoomRepo, notifs feed.NotificationRepo, engine feed.Engine) *Service {
	return &Service{rooms: rooms, notifs: notifs, engine: engine}
}

// WithPremiumRooms wires the premium/private-room paywall feature —
// optional, so a caller that never enables it (or tests that don't care)
// doesn't need to construct the extra repo/closure.
func (s *Service) WithPremiumRooms(unlocks feed.PremiumUnlockRepo, unlockCoins UnlockCoinsFunc) *Service {
	s.unlocks = unlocks
	s.unlockCoins = unlockCoins
	return s
}

// ─── Feed ─────────────────────────────────────────────────────────────────────

// GetFeed returns a paginated feed page for the given tab.
//
// BT-05 implementation: cursor encodes a snapshot of the ranked room IDs.
// Subsequent pages decode the snapshot and slice from it — so the ordering
// is stable even as viewer counts change between page fetches.
// A fresh ranking is only computed on the first page (no cursor) or when the
// cursor has expired (>5 min).
func (s *Service) GetFeed(ctx context.Context, tab feed.Tab, user feed.UserFeedContext, cursor string, limit int) (*feed.FeedPage, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	switch tab {
	case feed.TabForYou, feed.TabHot, feed.TabExplore:
		return s.getRankedFeed(ctx, tab, user, cursor, limit)
	case feed.TabFollowing:
		return s.getFollowingFeed(ctx, user, cursor, limit)
	default:
		return nil, fmt.Errorf("feedsvc: unknown tab %q", tab)
	}
}

func (s *Service) getRankedFeed(ctx context.Context, tab feed.Tab, user feed.UserFeedContext, rawCursor string, limit int) (*feed.FeedPage, error) {
	var rankedIDs []string
	offset := 0

	decoded := feed.DecodeCursor(rawCursor)
	if decoded != nil && decoded.Tab == tab {
		// Re-use the snapshot from the cursor — stable pagination (BT-05).
		rankedIDs = decoded.RankedIDs
		offset = decoded.Offset
	} else {
		// First page or expired cursor — compute fresh ranking.
		rooms, err := s.rooms.ListLive(ctx)
		if err != nil {
			return nil, fmt.Errorf("feedsvc: list live: %w", err)
		}

		// Hot tab: sort by engagement only (viewer count + gift velocity), not personalised.
		if tab == feed.TabHot {
			user = feed.UserFeedContext{} // strip personalisation
		}

		ranked := s.engine.Rank(ctx, rooms, user)
		rankedIDs = make([]string, len(ranked))
		for i, r := range ranked {
			rankedIDs[i] = r.RoomID
		}
	}

	// Slice the snapshot.
	if offset >= len(rankedIDs) {
		return &feed.FeedPage{Items: []feed.RoomCard{}, Tab: tab}, nil
	}
	pageIDs := rankedIDs[offset:]
	if len(pageIDs) > limit {
		pageIDs = pageIDs[:limit]
	}

	// Fetch current room state for the IDs in this page.
	// Viewer counts may have changed since the snapshot — that's fine,
	// but the ORDER is frozen by the snapshot.
	items := make([]feed.RoomCard, 0, len(pageIDs))
	for _, id := range pageIDs {
		room, err := s.rooms.Get(ctx, id)
		if err != nil {
			continue // room ended between snapshot and fetch — skip silently
		}
		if room.Status != feed.RoomLive {
			continue
		}
		items = append(items, room.ToCard())
	}

	// Build next cursor.
	nextOffset := offset + len(pageIDs)
	var nextCursor string
	if nextOffset < len(rankedIDs) {
		nextCursor = feed.EncodeCursor(feed.CursorPayload{
			RankedIDs: rankedIDs,
			Offset:    nextOffset,
			Tab:       tab,
			CreatedAt: time.Now(),
		})
	}

	return &feed.FeedPage{
		Items:      items,
		NextCursor: nextCursor,
		Tab:        tab,
		Total:      len(rankedIDs),
	}, nil
}

func (s *Service) getFollowingFeed(ctx context.Context, user feed.UserFeedContext, _ string, limit int) (*feed.FeedPage, error) {
	if len(user.FollowedHostIDs) == 0 {
		return &feed.FeedPage{Items: []feed.RoomCard{}, Tab: feed.TabFollowing}, nil
	}
	rooms, err := s.rooms.ListByHosts(ctx, user.FollowedHostIDs)
	if err != nil {
		return nil, fmt.Errorf("feedsvc: list by hosts: %w", err)
	}
	// Following tab: sort by time started (most recent first), not by score.
	sortByStarted(rooms)
	if len(rooms) > limit {
		rooms = rooms[:limit]
	}
	items := make([]feed.RoomCard, len(rooms))
	for i, r := range rooms {
		items[i] = r.ToCard()
	}
	return &feed.FeedPage{Items: items, Tab: feed.TabFollowing}, nil
}

// ─── Search ───────────────────────────────────────────────────────────────────

// Search searches users, rooms, and tags matching the query.
// Returns results deduplicated and ranked by relevance.
func (s *Service) Search(ctx context.Context, query string, types []string, limit int) ([]feed.SearchResult, error) {
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, errors.New("feedsvc: query must be at least 2 characters")
	}
	if limit <= 0 || limit > 30 {
		limit = 20
	}

	typeSet := toSet(types)
	wantRooms := len(typeSet) == 0 || typeSet["room"]

	var results []feed.SearchResult

	if wantRooms {
		rooms, err := s.rooms.ListLive(ctx)
		if err != nil {
			return nil, err
		}
		q := strings.ToLower(query)
		for _, room := range rooms {
			if strings.Contains(strings.ToLower(room.Title), q) ||
				strings.Contains(strings.ToLower(room.HostName), q) ||
				containsTag(room.Tags, q) {
				vc := fmt.Sprintf("%d viewers", room.ViewerCount)
				results = append(results, feed.SearchResult{
					Type:        "room",
					ID:          room.RoomID,
					DisplayName: room.HostName + " — " + room.Title,
					Subtitle:    vc,
					IsLive:      room.Status == feed.RoomLive,
				})
			}
			if len(results) >= limit {
				break
			}
		}
	}

	return results, nil
}

// ─── Rooms ────────────────────────────────────────────────────────────────────

// IsRoomUnlockedForViewer reports whether viewerID can see roomID's
// content: true immediately if the room isn't premium, if viewerID is
// empty (never claims anonymous/guest access — callers must treat "" as
// not unlocked), or if viewerID is the room's own host; otherwise
// defers to the unlock ledger.
func (s *Service) IsRoomUnlockedForViewer(ctx context.Context, room *feed.Room, viewerID string) (bool, error) {
	if !room.IsPremium {
		return true, nil
	}
	if viewerID == "" {
		return false, nil
	}
	if viewerID == room.HostID {
		return true, nil
	}
	if s.unlocks == nil {
		return false, nil
	}
	return s.unlocks.HasUnlocked(ctx, room.RoomID, viewerID)
}

// SetRoomPremium turns a room's paywall on/off. Host-only — hostID must
// match the room's own HostID, enforced here rather than trusted from
// the caller.
func (s *Service) SetRoomPremium(ctx context.Context, hostID, roomID string, isPremium bool, unlockPriceCoins int64) (*feed.Room, error) {
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		if errors.Is(err, feed.ErrRoomNotFound) {
			return nil, ErrRoomNotFound
		}
		return nil, fmt.Errorf("feedsvc: get room: %w", err)
	}
	if room.HostID != hostID {
		return nil, ErrNotRoomHost
	}
	if isPremium && unlockPriceCoins <= 0 {
		return nil, ErrInvalidUnlockPrice
	}
	if !isPremium {
		unlockPriceCoins = 0
	}
	if err := s.rooms.SetPremium(ctx, roomID, isPremium, unlockPriceCoins); err != nil {
		return nil, fmt.Errorf("feedsvc: set premium: %w", err)
	}
	room.IsPremium = isPremium
	room.UnlockPriceCoins = unlockPriceCoins
	return room, nil
}

// UnlockRoom charges viewerID room.UnlockPriceCoins (once — a repeat
// call for a room already unlocked is a cheap no-op, not a re-charge)
// and records the unlock. idempotencyKey follows the same
// retry-returns-the-same-result contract as every other money-moving
// endpoint in this codebase.
func (s *Service) UnlockRoom(ctx context.Context, viewerID, roomID, idempotencyKey string) (*feed.Room, error) {
	if s.unlocks == nil || s.unlockCoins == nil {
		return nil, ErrPremiumFeatureDisabled
	}
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		if errors.Is(err, feed.ErrRoomNotFound) {
			return nil, ErrRoomNotFound
		}
		return nil, fmt.Errorf("feedsvc: get room: %w", err)
	}
	if !room.IsPremium || viewerID == room.HostID {
		return room, nil
	}
	already, err := s.unlocks.HasUnlocked(ctx, roomID, viewerID)
	if err != nil {
		return nil, err
	}
	if already {
		return room, nil
	}
	if _, err := s.unlockCoins(ctx, viewerID, room.HostID, room.UnlockPriceCoins, idempotencyKey); err != nil {
		return nil, err
	}
	if err := s.unlocks.RecordUnlock(ctx, roomID, viewerID); err != nil {
		return nil, err
	}
	return room, nil
}

// GetRoom returns a single room for the room-view screen.
func (s *Service) GetRoom(ctx context.Context, roomID string) (*feed.Room, error) {
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		if errors.Is(err, feed.ErrRoomNotFound) {
			return nil, ErrRoomNotFound
		}
		return nil, fmt.Errorf("feedsvc: get room: %w", err)
	}
	return room, nil
}

// CreateRoom creates a new live room. Called when a broadcaster goes live.
// Gate check (age assurance, T&S standing) is enforced by the auth middleware
// before this is called.
func (s *Service) CreateRoom(ctx context.Context, hostID, hostName string, title, language, region string, tags []string) (*feed.Room, error) {
	room := feed.Room{
		HostID:     hostID,
		HostName:   hostName,
		Title:      title,
		Language:   language,
		RegionCode: region,
		Tags:       tags,
		Status:     feed.RoomLive,
	}
	return s.rooms.Create(ctx, room)
}

// EndRoom ends a live room.
func (s *Service) EndRoom(ctx context.Context, roomID, hostID, reason string) error {
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		return ErrRoomNotFound
	}
	if room.HostID != hostID {
		return errors.New("feedsvc: only the host can end a room")
	}
	return s.rooms.End(ctx, roomID, reason)
}

// UpdateViewerCount persists a room's live viewer count — called by the
// realtime gateway's WithViewerCount hook (gateway/ws) every time a
// client subscribes/unsubscribes from a room topic, so GET /feed and
// GET /rooms/{id} report the same number of viewers the room's chat
// actually has connected, instead of whatever the count was at room
// creation.
func (s *Service) UpdateViewerCount(ctx context.Context, roomID string, count int) error {
	return s.rooms.UpdateViewerCount(ctx, roomID, count)
}

// ─── Notifications ────────────────────────────────────────────────────────────

func (s *Service) GetNotifications(ctx context.Context, accountID, cursor string, limit int) ([]feed.Notification, string, error) {
	return s.notifs.List(ctx, accountID, cursor, limit)
}

func (s *Service) MarkNotificationsRead(ctx context.Context, accountID, upToID string) error {
	return s.notifs.MarkRead(ctx, accountID, upToID)
}

func (s *Service) GetNotificationSummary(ctx context.Context, accountID string) (*feed.NotificationSummary, error) {
	return s.notifs.GetSummary(ctx, accountID)
}

// ─── Errors ───────────────────────────────────────────────────────────────────

var ErrRoomNotFound = errors.New("feedsvc: room not found")
var ErrNotRoomHost = errors.New("feedsvc: only the room's host can do this")
var ErrInvalidUnlockPrice = errors.New("feedsvc: a premium room needs a positive unlock price")
var ErrPremiumFeatureDisabled = errors.New("feedsvc: premium rooms are not enabled on this server")

// ─── Helpers ──────────────────────────────────────────────────────────────────

func sortByStarted(rooms []feed.Room) {
	for i := 0; i < len(rooms)-1; i++ {
		for j := i + 1; j < len(rooms); j++ {
			ti, tj := time.Time{}, time.Time{}
			if rooms[i].StartedAt != nil {
				ti = *rooms[i].StartedAt
			}
			if rooms[j].StartedAt != nil {
				tj = *rooms[j].StartedAt
			}
			if ti.Before(tj) {
				rooms[i], rooms[j] = rooms[j], rooms[i]
			}
		}
	}
}

func containsTag(tags []string, q string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
