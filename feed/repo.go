// Package feed — repository interfaces and in-memory implementations.
package feed

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// ─── Repository interfaces ────────────────────────────────────────────────────

// RoomRepo is the data-access contract for rooms.
type RoomRepo interface {
	// ListLive returns all currently live rooms, enriched with host info and metrics.
	// Used as the candidate set for the ranking engine.
	ListLive(ctx context.Context) ([]Room, error)

	// Get returns a single room by ID.
	Get(ctx context.Context, roomID string) (*Room, error)

	// GetByHost returns the currently-live room for a host, if any.
	GetByHost(ctx context.Context, hostID string) (*Room, error)

	// ListByHosts returns live rooms for a set of host IDs (for the Following tab).
	ListByHosts(ctx context.Context, hostIDs []string) ([]Room, error)

	// Create creates a new room (called when a broadcaster goes live).
	Create(ctx context.Context, room Room) (*Room, error)

	// UpdateViewerCount updates the denormalized viewer count.
	UpdateViewerCount(ctx context.Context, roomID string, count int) error

	// End marks a room as ended.
	End(ctx context.Context, roomID string, reason string) error

	// SetPremium flips a room's paywall on/off and sets its one-time
	// unlock price — host-only, enforced by feedsvc.
	SetPremium(ctx context.Context, roomID string, isPremium bool, unlockPriceCoins int64) error
}

// PremiumUnlockRepo tracks which viewers have paid to unlock which
// premium rooms — a one-time unlock per (room, viewer) pair, not a
// per-minute billing relationship (that's what PV calls are for).
type PremiumUnlockRepo interface {
	HasUnlocked(ctx context.Context, roomID, accountID string) (bool, error)
	RecordUnlock(ctx context.Context, roomID, accountID string) error
}

// NotificationRepo is the data-access contract for notifications.
type NotificationRepo interface {
	List(ctx context.Context, accountID string, cursor string, limit int) ([]Notification, string, error)
	MarkRead(ctx context.Context, accountID string, upToID string) error
	GetSummary(ctx context.Context, accountID string) (*NotificationSummary, error)
	Create(ctx context.Context, n Notification) error
}

var ErrRoomNotFound = errors.New("feed: room not found")

// ─── In-memory RoomRepo ───────────────────────────────────────────────────────

type MemRoomRepo struct {
	mu    sync.RWMutex
	rooms map[string]*Room
	seq   int
}

func NewMemRoomRepo() *MemRoomRepo {
	r := &MemRoomRepo{rooms: make(map[string]*Room)}
	r.seedTestRooms()
	return r
}

func (r *MemRoomRepo) ListLive(_ context.Context) ([]Room, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Room
	for _, room := range r.rooms {
		if room.Status == RoomLive {
			cp := *room
			out = append(out, cp)
		}
	}
	return out, nil
}

func (r *MemRoomRepo) Get(_ context.Context, roomID string) (*Room, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	room, ok := r.rooms[roomID]
	if !ok {
		return nil, ErrRoomNotFound
	}
	cp := *room
	return &cp, nil
}

func (r *MemRoomRepo) GetByHost(_ context.Context, hostID string) (*Room, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, room := range r.rooms {
		if room.HostID == hostID && room.Status == RoomLive {
			cp := *room
			return &cp, nil
		}
	}
	return nil, ErrRoomNotFound
}

func (r *MemRoomRepo) ListByHosts(_ context.Context, hostIDs []string) ([]Room, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hostSet := make(map[string]bool, len(hostIDs))
	for _, id := range hostIDs {
		hostSet[id] = true
	}
	var out []Room
	for _, room := range r.rooms {
		if room.Status == RoomLive && hostSet[room.HostID] {
			cp := *room
			out = append(out, cp)
		}
	}
	return out, nil
}

func (r *MemRoomRepo) Create(_ context.Context, room Room) (*Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	room.RoomID = roomID(r.seq)
	now := time.Now()
	room.StartedAt = &now
	room.Status = RoomLive
	r.rooms[room.RoomID] = &room
	cp := room
	return &cp, nil
}

func (r *MemRoomRepo) UpdateViewerCount(_ context.Context, roomID string, count int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[roomID]
	if !ok {
		return ErrRoomNotFound
	}
	room.ViewerCount = count
	if count > room.PeakViewers {
		room.PeakViewers = count
	}
	return nil
}

func (r *MemRoomRepo) End(_ context.Context, roomID string, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[roomID]
	if !ok {
		return ErrRoomNotFound
	}
	room.Status = RoomEnded
	now := time.Now()
	room.EndedAt = &now
	return nil
}

func (r *MemRoomRepo) SetPremium(_ context.Context, roomID string, isPremium bool, unlockPriceCoins int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[roomID]
	if !ok {
		return ErrRoomNotFound
	}
	room.IsPremium = isPremium
	room.UnlockPriceCoins = unlockPriceCoins
	return nil
}

// ─── In-memory PremiumUnlockRepo ───────────────────────────────────────────────

type MemPremiumUnlockRepo struct {
	mu       sync.RWMutex
	unlocked map[string]bool // "roomID:accountID" -> true
}

func NewMemPremiumUnlockRepo() *MemPremiumUnlockRepo {
	return &MemPremiumUnlockRepo{unlocked: make(map[string]bool)}
}

var _ PremiumUnlockRepo = (*MemPremiumUnlockRepo)(nil)

func (r *MemPremiumUnlockRepo) HasUnlocked(_ context.Context, roomID, accountID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.unlocked[roomID+":"+accountID], nil
}

func (r *MemPremiumUnlockRepo) RecordUnlock(_ context.Context, roomID, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unlocked[roomID+":"+accountID] = true
	return nil
}

// seedTestRooms populates realistic test data for the dev server.
func (r *MemRoomRepo) seedTestRooms() {
	regions := []string{"US", "PK", "IN", "BR", "NG", "PH", "ID", "MX", "EG", "TR"}
	languages := []string{"en", "ur", "hi", "pt", "yo", "tl", "id", "es", "ar", "tr"}
	tags := [][]string{
		{"music", "live"}, {"chat", "q&a"}, {"gaming"}, {"dance"},
		{"comedy"}, {"talk"}, {"beauty"}, {"fitness"}, {"cooking"}, {"travel"},
	}
	hosts := []struct{ id, name string }{
		{"host-001", "Aisha_Streams"}, {"host-002", "Carlos_Live"}, {"host-003", "Priya_Music"},
		{"host-004", "Yuki_Gaming"}, {"host-005", "Fatima_Talk"}, {"host-006", "Diego_Comedy"},
		{"host-007", "Nia_Dance"}, {"host-008", "Omar_Beats"}, {"host-009", "Sofia_Beauty"},
		{"host-010", "Raj_Fitness"}, {"host-011", "Mei_Cook"}, {"host-012", "Ibrahim_Chat"},
		{"host-013", "Lila_Travel"}, {"host-014", "Chidi_Music"}, {"host-015", "Ana_Gaming"},
	}
	titles := []string{
		"Late night vibes 🎵", "Come chat with me!", "Gaming w/ viewers",
		"Dance practice 💃", "Story time + Q&A", "Comedy hour 😂",
		"New makeup tutorial", "Morning workout 💪", "Cooking for 100!", "Traveling live 🌍",
		"Beat making session", "Just chatting ✨", "Chill stream 🍵", "Music cover night",
		"Let's play together",
	}

	rng := rand.New(rand.NewSource(42)) // deterministic seed for tests
	now := time.Now()

	for i, host := range hosts {
		startedAt := now.Add(-time.Duration(rng.Intn(120)) * time.Minute)
		r.seq++
		roomID := roomID(r.seq)
		viewers := rng.Intn(8000) + 10
		r.rooms[roomID] = &Room{
			RoomID:             roomID,
			HostID:             host.id,
			HostName:           host.name,
			HostLevel:          rng.Intn(50) + 1,
			Title:              titles[i%len(titles)],
			Tags:               tags[i%len(tags)],
			Language:           languages[i%len(languages)],
			RegionCode:         regions[i%len(regions)],
			ViewerCount:        viewers,
			PeakViewers:        viewers + rng.Intn(500),
			Status:             RoomLive,
			StartedAt:          &startedAt,
			GiftVelocity:       rng.Float64() * 8,
			FollowerGrowthRate: rng.Float64() * 3,
		}
	}
}

func roomID(seq int) string {
	return "room-" + strings.ReplaceAll(time.Now().Format("20060102")+"-"+itoa(seq), " ", "")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 8)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ─── In-memory NotificationRepo ───────────────────────────────────────────────

type MemNotificationRepo struct {
	mu   sync.RWMutex
	data map[string][]Notification // accountID → notifications
	seq  int
}

func NewMemNotificationRepo() *MemNotificationRepo {
	return &MemNotificationRepo{data: make(map[string][]Notification)}
}

func (r *MemNotificationRepo) List(_ context.Context, accountID, cursor string, limit int) ([]Notification, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	notifs := r.data[accountID]
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	// Cursor is index into the slice (most-recent first)
	start := 0
	if cursor != "" {
		for i, n := range notifs {
			if n.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	if start >= len(notifs) {
		return nil, "", nil
	}
	slice := notifs[start:]
	var nextCursor string
	if len(slice) > limit {
		nextCursor = slice[limit].ID
		slice = slice[:limit]
	}
	out := make([]Notification, len(slice))
	copy(out, slice)
	return out, nextCursor, nil
}

func (r *MemNotificationRepo) MarkRead(_ context.Context, accountID, upToID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	notifs := r.data[accountID]
	marking := false
	for i := range notifs {
		if notifs[i].ID == upToID {
			marking = true
		}
		if marking {
			notifs[i].Read = true
		}
	}
	return nil
}

func (r *MemNotificationRepo) GetSummary(_ context.Context, accountID string) (*NotificationSummary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var count int
	for _, n := range r.data[accountID] {
		if !n.Read {
			count++
		}
	}
	return &NotificationSummary{UnreadCount: count}, nil
}

func (r *MemNotificationRepo) Create(_ context.Context, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	if n.ID == "" {
		n.ID = "notif-" + itoa(r.seq)
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	// Prepend (newest first)
	if n.ActorID != nil {
		r.data[*n.ActorID] = append([]Notification{n}, r.data[*n.ActorID]...)
	}
	return nil
}
