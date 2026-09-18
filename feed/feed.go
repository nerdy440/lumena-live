// Package feed defines the discovery and feed domain.
// Three tabs: ForYou (personalized), Hot (trending), Explore (browse), Following.
// Feed state — cursor, scroll offset, active tab — must survive round trips (BT-05).
package feed

import (
	"time"
)

// Tab identifies which feed the client is viewing.
type Tab string

const (
	TabForYou    Tab = "for_you"
	TabHot       Tab = "hot"
	TabExplore   Tab = "explore"
	TabFollowing Tab = "following"
)

// RoomStatus mirrors the DB enum for rooms.
type RoomStatus string

const (
	RoomLive       RoomStatus = "live"
	RoomScheduled  RoomStatus = "scheduled"
	RoomEnded      RoomStatus = "ended"
)

// RoomCard is a feed item — everything the client needs to render a room card
// and enter the room. Nothing more: gift velocity and score reasons are internal.
type RoomCard struct {
	RoomID      string     `json:"room_id"`
	HostID      string     `json:"host_id"`
	HostName    string     `json:"host_name"`
	HostAvatar  *string    `json:"host_avatar"`
	HostLevel   int        `json:"host_level"`
	Title       string     `json:"title"`
	CoverURL    *string    `json:"cover_url"`
	Tags        []string   `json:"tags"`
	Language    string     `json:"language"`
	RegionCode  string     `json:"region_code"`
	ViewerCount int        `json:"viewer_count"`
	Status      RoomStatus `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	// IsPremium/UnlockPriceCoins describe the room's paywall (doc-less new
	// feature: host-gated "premium" rooms a viewer pays a one-time coin
	// price to unlock — see feedsvc.UnlockRoom). IsUnlocked is only
	// meaningful together with a specific viewer, so ToCard() alone never
	// sets it true — see handlers.GetRoom, which fills it in per-viewer
	// after the fact; every OTHER caller of ToCard() (feed listings) gets
	// it as false, which is the correct "haven't checked" default there
	// too, since list views don't show paywalled content anyway.
	IsPremium        bool  `json:"is_premium"`
	UnlockPriceCoins int64 `json:"unlock_price_coins,omitempty"`
	IsUnlocked       bool  `json:"is_unlocked"`
	// Score is NEVER serialized to clients — internal ranking signal only.
	score float64
}

// FeedPage is one page of feed results with an opaque cursor.
// The cursor encodes the ranking snapshot so pagination is stable
// even as rooms change their live viewer counts.
type FeedPage struct {
	Items      []RoomCard `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
	Tab        Tab        `json:"tab"`
	Total      int        `json:"total,omitempty"` // approximate, for UI only
}

// UserFeedContext carries the signals used to personalize the feed.
type UserFeedContext struct {
	AccountID          string
	Languages          []string
	Interests          []string
	RegionCode         string
	FollowedHostIDs    []string
	RecentlyViewedIDs  []string // room IDs viewed in last 24h
}

// GuestFeedContext is used when no account is logged in.
// Personalization falls back to region (from IP) and language (Accept-Language).
func GuestFeedContext(regionCode, language string) UserFeedContext {
	return UserFeedContext{
		RegionCode: regionCode,
		Languages:  []string{language},
	}
}

// Room is the full room record (superset of RoomCard, used internally).
type Room struct {
	RoomID       string
	HostID       string
	HostName     string
	HostAvatar   *string
	HostLevel    int
	Title        string
	CoverURL     *string
	Tags         []string
	Language     string
	RegionCode   string
	ViewerCount  int
	PeakViewers  int
	Status       RoomStatus
	StartedAt    *time.Time
	EndedAt      *time.Time
	// Internal scoring inputs — never serialized to clients
	GiftVelocity       float64 // gifts per minute in last 5 min
	FollowerGrowthRate float64 // new follows per minute
	IsFollowedHost     bool    // set by the feed service when ctx has followed hosts

	IsPremium        bool
	UnlockPriceCoins int64
}

// toCard converts a Room to a RoomCard for API responses.
// Explicitly omits all internal fields.
func (r Room) ToCard() RoomCard {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	return RoomCard{
		RoomID:           r.RoomID,
		HostID:           r.HostID,
		HostName:         r.HostName,
		HostAvatar:       r.HostAvatar,
		HostLevel:        r.HostLevel,
		Title:            r.Title,
		CoverURL:         r.CoverURL,
		Tags:             tags,
		Language:         r.Language,
		RegionCode:       r.RegionCode,
		ViewerCount:      r.ViewerCount,
		Status:           r.Status,
		StartedAt:        r.StartedAt,
		IsPremium:        r.IsPremium,
		UnlockPriceCoins: r.UnlockPriceCoins,
	}
}

// SearchResult is a user or room returned from search.
type SearchResult struct {
	Type        string  `json:"type"` // "user" | "room" | "tag"
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	Subtitle    string  `json:"subtitle,omitempty"` // follower count, viewer count, etc.
	IsLive      bool    `json:"is_live,omitempty"`
}

// Notification is a single notification item.
type Notification struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // "follow" | "gift" | "room_live" | "system"
	ActorID   *string   `json:"actor_id,omitempty"`
	ActorName *string   `json:"actor_name,omitempty"`
	Body      string    `json:"body"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
	DeepLink  string    `json:"deep_link,omitempty"`
}

// NotificationSummary is the badge count returned in the feed header.
type NotificationSummary struct {
	UnreadCount int `json:"unread_count"`
}
