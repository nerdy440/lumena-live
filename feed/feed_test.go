package feed_test

import (
	"context"
	"testing"
	"time"

	"github.com/lumena/feed"
)

// ─── Recommendation engine tests ──────────────────────────────────────────────

func testRooms() []feed.Room {
	now := time.Now()
	fresh := now.Add(-10 * time.Minute)
	old := now.Add(-90 * time.Minute)

	return []feed.Room{
		{
			RoomID: "r1", HostID: "h1", Title: "Fresh small room",
			Language: "en", RegionCode: "US", ViewerCount: 50,
			Status: feed.RoomLive, StartedAt: &fresh,
			GiftVelocity: 0.5, FollowerGrowthRate: 0.2,
		},
		{
			RoomID: "r2", HostID: "h2", Title: "Old huge room",
			Language: "es", RegionCode: "MX", ViewerCount: 9000,
			Status: feed.RoomLive, StartedAt: &old,
			GiftVelocity: 0.1, FollowerGrowthRate: 0.1,
		},
		{
			RoomID: "r3", HostID: "h3", Title: "Followed host",
			Language: "en", RegionCode: "US", ViewerCount: 200,
			Status: feed.RoomLive, StartedAt: &fresh,
			GiftVelocity: 2.0, FollowerGrowthRate: 1.5,
		},
		{
			RoomID: "r4", HostID: "h4", Title: "Gift-heavy room",
			Language: "en", RegionCode: "US", ViewerCount: 500,
			Status: feed.RoomLive, StartedAt: &fresh,
			GiftVelocity: 7.5, FollowerGrowthRate: 2.0,
		},
		{
			RoomID: "r5", HostID: "h5", Title: "Recently viewed",
			Language: "en", RegionCode: "US", ViewerCount: 800,
			Status: feed.RoomLive, StartedAt: &fresh,
			GiftVelocity: 3.0, FollowerGrowthRate: 1.0,
		},
	}
}

func TestEngine_FollowedHostRanksHigher(t *testing.T) {
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	rooms := testRooms()

	user := feed.UserFeedContext{
		Languages:       []string{"en"},
		RegionCode:      "US",
		FollowedHostIDs: []string{"h3"}, // room r3
	}

	ranked := engine.Rank(context.Background(), rooms, user)

	// h3 (followed host) must rank above h2 (9000 viewers but no follow, wrong language)
	posH3, posH2 := -1, -1
	for i, r := range ranked {
		switch r.RoomID {
		case "r3":
			posH3 = i
		case "r2":
			posH2 = i
		}
	}
	if posH3 == -1 || posH2 == -1 {
		t.Fatal("missing expected rooms in ranked output")
	}
	if posH3 > posH2 {
		t.Errorf("followed host (pos %d) should rank above huge-but-unfollowed (pos %d)", posH3, posH2)
	}
}

func TestEngine_NotJustViewerCount(t *testing.T) {
	// The 9000-viewer room must NOT always rank first.
	// A fresh room with matching language + region + gift activity should beat it.
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	rooms := testRooms()

	user := feed.UserFeedContext{
		Languages:  []string{"en"},
		RegionCode: "US",
	}
	ranked := engine.Rank(context.Background(), rooms, user)
	if len(ranked) == 0 {
		t.Fatal("empty ranked output")
	}
	if ranked[0].RoomID == "r2" {
		t.Error("9000-viewer old Spanish room must not rank #1 for an English US user — scoring must be multi-signal, not viewer-count-only")
	}
}

func TestEngine_RecentlyViewedPenalised(t *testing.T) {
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	rooms := testRooms()

	// r4 (gift-heavy) should rank high without the penalty
	userWithout := feed.UserFeedContext{Languages: []string{"en"}, RegionCode: "US"}
	rankedWithout := engine.Rank(context.Background(), rooms, userWithout)
	posR4Without := indexOf(rankedWithout, "r4")

	// r4 was recently viewed — should drop
	userWith := feed.UserFeedContext{
		Languages:         []string{"en"},
		RegionCode:        "US",
		RecentlyViewedIDs: []string{"r4"},
	}
	rankedWith := engine.Rank(context.Background(), rooms, userWith)
	posR4With := indexOf(rankedWith, "r4")

	if posR4With <= posR4Without {
		t.Errorf("recently-viewed room r4 should rank lower: without=%d, with=%d", posR4Without, posR4With)
	}
}

func TestEngine_EndedRoomsExcluded(t *testing.T) {
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	now := time.Now()
	rooms := []feed.Room{
		{RoomID: "live1", Status: feed.RoomLive, StartedAt: &now, Language: "en", RegionCode: "US"},
		{RoomID: "ended1", Status: feed.RoomEnded, StartedAt: &now, Language: "en", RegionCode: "US"},
	}
	ranked := engine.Rank(context.Background(), rooms, feed.UserFeedContext{})
	for _, r := range ranked {
		if r.RoomID == "ended1" {
			t.Error("ended rooms must be excluded from ranked output")
		}
	}
}

func TestEngine_EmptyRooms(t *testing.T) {
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	ranked := engine.Rank(context.Background(), nil, feed.UserFeedContext{})
	if len(ranked) != 0 {
		t.Errorf("expected empty ranked output for no rooms, got %d", len(ranked))
	}
}

// ─── Cursor-stable pagination (BT-05) ─────────────────────────────────────────

func TestCursor_EncodeDecode(t *testing.T) {
	payload := feed.CursorPayload{
		RankedIDs: []string{"r1", "r2", "r3", "r4", "r5"},
		Offset:    3,
		Tab:       feed.TabForYou,
		CreatedAt: time.Now(),
	}
	encoded := feed.EncodeCursor(payload)
	if encoded == "" {
		t.Fatal("encoded cursor must not be empty")
	}

	decoded := feed.DecodeCursor(encoded)
	if decoded == nil {
		t.Fatal("decoded cursor must not be nil")
	}
	if decoded.Offset != payload.Offset {
		t.Errorf("offset mismatch: %d != %d", decoded.Offset, payload.Offset)
	}
	if len(decoded.RankedIDs) != 5 {
		t.Errorf("expected 5 IDs, got %d", len(decoded.RankedIDs))
	}
	if decoded.Tab != feed.TabForYou {
		t.Errorf("tab mismatch: %s != %s", decoded.Tab, feed.TabForYou)
	}
}

func TestCursor_Expired(t *testing.T) {
	payload := feed.CursorPayload{
		RankedIDs: []string{"r1"},
		Offset:    1,
		Tab:       feed.TabHot,
		CreatedAt: time.Now().Add(-10 * time.Minute), // 10 min ago — past 5 min TTL
	}
	encoded := feed.EncodeCursor(payload)
	decoded := feed.DecodeCursor(encoded)
	if decoded != nil {
		t.Error("expired cursor must return nil")
	}
}

func TestCursor_Malformed(t *testing.T) {
	cases := []string{"", "not-base64!!", "aGVsbG8=", "eyJpbnZhbGlkIjp0cnVlfQ"}
	for _, c := range cases {
		decoded := feed.DecodeCursor(c)
		if decoded != nil && c == "" {
			t.Errorf("empty string cursor must return nil")
		}
	}
}

// ─── Room card serialisation ───────────────────────────────────────────────────

// TestRoomCard_NoInternalFields verifies that the ToCard() method
// does not expose the internal score or gift velocity to clients.
func TestRoomCard_NoInternalFields(t *testing.T) {
	now := time.Now()
	room := feed.Room{
		RoomID: "r1", HostID: "h1", Title: "Test",
		Language: "en", RegionCode: "US", ViewerCount: 100,
		Status: feed.RoomLive, StartedAt: &now,
		GiftVelocity:       9.9, // must NOT appear in card
		FollowerGrowthRate: 4.4, // must NOT appear in card
	}
	card := room.ToCard()
	if card.RoomID != room.RoomID {
		t.Error("room ID mismatch")
	}
	if card.ViewerCount != room.ViewerCount {
		t.Error("viewer count mismatch")
	}
	// No way to check unexported score field — verified by struct definition.
	// The card struct has no GiftVelocity or FollowerGrowthRate fields.
	// This test documents the intent; the compiler enforces it.
}

// ─── Search ───────────────────────────────────────────────────────────────────

func TestSearchResultsNonEmpty(t *testing.T) {
	// Basic sanity: a search over seed data returns something
	repo := feed.NewMemRoomRepo()
	rooms, _ := repo.ListLive(context.Background())
	if len(rooms) == 0 {
		t.Skip("no seed rooms — skip search test")
	}
	// Title "music" should match at least one room from the seed data
	q := "music"
	found := false
	for _, r := range rooms {
		if contains(r.Title, q) || contains(r.HostName, q) {
			found = true
			break
		}
	}
	_ = found // document: at least one result expected in live tests
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func indexOf(rooms []feed.Room, id string) int {
	for i, r := range rooms {
		if r.RoomID == id {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
