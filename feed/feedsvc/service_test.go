package feedsvc_test

import (
	"context"
	"testing"

	"github.com/lumena/feed"
	"github.com/lumena/feed/feedsvc"
)

func newSvc() *feedsvc.Service {
	rooms := feed.NewMemRoomRepo()
	notifs := feed.NewMemNotificationRepo()
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	return feedsvc.NewService(rooms, notifs, engine)
}

// ─── Feed pages ───────────────────────────────────────────────────────────────

func TestGetFeed_ForYou_ReturnsCards(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	user := feed.UserFeedContext{Languages: []string{"en"}, RegionCode: "US"}

	page, err := svc.GetFeed(ctx, feed.TabForYou, user, "", 10)
	if err != nil {
		t.Fatalf("get feed: %v", err)
	}
	if len(page.Items) == 0 {
		t.Error("expected feed items from seeded data")
	}
	if page.Tab != feed.TabForYou {
		t.Errorf("tab mismatch: %s", page.Tab)
	}
}

func TestGetFeed_Hot_ReturnsItems(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	page, err := svc.GetFeed(ctx, feed.TabHot, feed.UserFeedContext{}, "", 10)
	if err != nil {
		t.Fatalf("get hot feed: %v", err)
	}
	if len(page.Items) == 0 {
		t.Error("expected hot feed items")
	}
}

func TestGetFeed_Following_EmptyWhenNoFollows(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	user := feed.UserFeedContext{AccountID: "acc-001", FollowedHostIDs: nil}
	page, err := svc.GetFeed(ctx, feed.TabFollowing, user, "", 10)
	if err != nil {
		t.Fatalf("get following feed: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("expected empty following feed with no follows, got %d items", len(page.Items))
	}
}

// TestGetFeed_CursorStability verifies BT-05: paginating with a cursor
// returns a stable slice of the original ranking — not a fresh re-rank.
func TestGetFeed_CursorStability(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	user := feed.UserFeedContext{Languages: []string{"en"}, RegionCode: "US"}

	// Page 1 — establishes the ranked snapshot
	page1, err := svc.GetFeed(ctx, feed.TabForYou, user, "", 5)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page1.NextCursor == "" {
		t.Skip("not enough rooms for pagination test (need >5 live rooms)")
	}

	// Page 2 — must use the same snapshot
	page2, err := svc.GetFeed(ctx, feed.TabForYou, user, page1.NextCursor, 5)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}

	// No item from page 1 should appear in page 2
	page1IDs := make(map[string]bool)
	for _, item := range page1.Items {
		page1IDs[item.RoomID] = true
	}
	for _, item := range page2.Items {
		if page1IDs[item.RoomID] {
			t.Errorf("BT-05: room %s appeared on both page 1 and page 2 — cursor is not stable", item.RoomID)
		}
	}
}

func TestGetFeed_InvalidTab(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	_, err := svc.GetFeed(ctx, feed.Tab("invalid"), feed.UserFeedContext{}, "", 10)
	if err == nil {
		t.Error("expected error for invalid tab")
	}
}

func TestGetFeed_LimitCapped(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	// Requesting 1000 items should be capped at 50
	page, err := svc.GetFeed(ctx, feed.TabHot, feed.UserFeedContext{}, "", 1000)
	if err != nil {
		t.Fatalf("get feed: %v", err)
	}
	if len(page.Items) > 50 {
		t.Errorf("limit must be capped at 50, got %d items", len(page.Items))
	}
}

// ─── Rooms ────────────────────────────────────────────────────────────────────

func TestGetRoom_Exists(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	// Get a room ID from the feed
	page, _ := svc.GetFeed(ctx, feed.TabHot, feed.UserFeedContext{}, "", 1)
	if len(page.Items) == 0 {
		t.Skip("no rooms in feed")
	}
	roomID := page.Items[0].RoomID

	room, err := svc.GetRoom(ctx, roomID)
	if err != nil {
		t.Fatalf("get room: %v", err)
	}
	if room.RoomID != roomID {
		t.Errorf("room ID mismatch: %s != %s", room.RoomID, roomID)
	}
}

func TestGetRoom_NotFound(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	_, err := svc.GetRoom(ctx, "nonexistent-room-id")
	if err == nil {
		t.Error("expected error for nonexistent room")
	}
}

func TestCreateRoom_ThenGetRoom(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	room, err := svc.CreateRoom(ctx, "host-999", "TestHost", "My test stream", "en", "US", []string{"test"})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if room.RoomID == "" {
		t.Error("expected non-empty room ID")
	}
	if room.Status != feed.RoomLive {
		t.Errorf("expected status=live, got %s", room.Status)
	}

	// Should be retrievable
	fetched, err := svc.GetRoom(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("get created room: %v", err)
	}
	if fetched.Title != "My test stream" {
		t.Errorf("title mismatch")
	}
}

func TestEndRoom_ByHost(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	room, _ := svc.CreateRoom(ctx, "host-999", "TestHost", "Stream", "en", "US", nil)
	if err := svc.EndRoom(ctx, room.RoomID, "host-999", "host_ended"); err != nil {
		t.Fatalf("end room: %v", err)
	}

	// Room should no longer appear in live feed
	page, _ := svc.GetFeed(ctx, feed.TabHot, feed.UserFeedContext{}, "", 100)
	for _, item := range page.Items {
		if item.RoomID == room.RoomID {
			t.Error("ended room must not appear in feed")
		}
	}
}

func TestEndRoom_NotByHost_Error(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	room, _ := svc.CreateRoom(ctx, "host-999", "TestHost", "Stream", "en", "US", nil)
	err := svc.EndRoom(ctx, room.RoomID, "not-the-host", "")
	if err == nil {
		t.Error("expected error when non-host tries to end a room")
	}
}

// ─── Search ───────────────────────────────────────────────────────────────────

func TestSearch_MatchesTitle(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	// "gaming" appears in the seed data
	results, err := svc.Search(ctx, "gaming", nil, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result for 'gaming'")
	}
}

func TestSearch_TooShortQuery(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	_, err := svc.Search(ctx, "a", nil, 10)
	if err == nil {
		t.Error("expected error for single-character query")
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	_, err := svc.Search(ctx, "", nil, 10)
	if err == nil {
		t.Error("expected error for empty query")
	}
}
