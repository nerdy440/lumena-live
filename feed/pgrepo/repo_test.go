package pgrepo_test

import (
	"context"
	"os"
	"testing"

	"github.com/lumena/db"
	"github.com/lumena/feed"
	"github.com/lumena/feed/pgrepo"
)

func newTestRepos(t *testing.T) (*pgrepo.RoomRepo, *pgrepo.PremiumUnlockRepo, *pgrepo.NotificationRepo) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.NewRoomRepo(pool), pgrepo.NewPremiumUnlockRepo(pool), pgrepo.NewNotificationRepo(pool)
}

func TestRoom_CreateGetUpdateViewersEndSetPremium(t *testing.T) {
	rooms, _, _ := newTestRepos(t)
	ctx := context.Background()

	room, err := rooms.Create(ctx, feed.Room{HostID: "alice", HostName: "Alice", Title: "hi", Tags: []string{"chat"}, Language: "en", RegionCode: "US"})
	if err != nil || room.RoomID == "" || room.Status != feed.RoomLive {
		t.Fatalf("create: %+v err=%v", room, err)
	}

	got, err := rooms.Get(ctx, room.RoomID)
	if err != nil || got.HostName != "Alice" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	byHost, err := rooms.GetByHost(ctx, "alice")
	if err != nil || byHost.RoomID != room.RoomID {
		t.Fatalf("get by host: %+v err=%v", byHost, err)
	}

	if err := rooms.UpdateViewerCount(ctx, room.RoomID, 42); err != nil {
		t.Fatalf("update viewers: %v", err)
	}
	afterViewers, _ := rooms.Get(ctx, room.RoomID)
	if afterViewers.ViewerCount != 42 || afterViewers.PeakViewers != 42 {
		t.Fatalf("viewer count/peak not updated: %+v", afterViewers)
	}
	_ = rooms.UpdateViewerCount(ctx, room.RoomID, 10)
	afterDrop, _ := rooms.Get(ctx, room.RoomID)
	if afterDrop.ViewerCount != 10 || afterDrop.PeakViewers != 42 {
		t.Fatalf("peak should stick at 42 even as viewer count drops: %+v", afterDrop)
	}

	if err := rooms.SetPremium(ctx, room.RoomID, true, 100); err != nil {
		t.Fatalf("set premium: %v", err)
	}
	premiumRoom, _ := rooms.Get(ctx, room.RoomID)
	if !premiumRoom.IsPremium || premiumRoom.UnlockPriceCoins != 100 {
		t.Fatalf("premium not set: %+v", premiumRoom)
	}

	live, err := rooms.ListLive(ctx)
	if err != nil || len(live) != 1 {
		t.Fatalf("list live: %+v err=%v", live, err)
	}

	if err := rooms.End(ctx, room.RoomID, "host left"); err != nil {
		t.Fatalf("end: %v", err)
	}
	ended, _ := rooms.Get(ctx, room.RoomID)
	if ended.Status != feed.RoomEnded || ended.EndedAt == nil {
		t.Fatalf("end not persisted: %+v", ended)
	}
	liveAfterEnd, _ := rooms.ListLive(ctx)
	if len(liveAfterEnd) != 0 {
		t.Fatalf("ended room should not be live anymore: %+v", liveAfterEnd)
	}
	if _, err := rooms.GetByHost(ctx, "alice"); err != feed.ErrRoomNotFound {
		t.Fatalf("GetByHost after end should be ErrRoomNotFound, got %v", err)
	}
}

func TestRoom_ListByHosts(t *testing.T) {
	rooms, _, _ := newTestRepos(t)
	ctx := context.Background()
	r1, _ := rooms.Create(ctx, feed.Room{HostID: "alice", HostName: "Alice"})
	_, _ = rooms.Create(ctx, feed.Room{HostID: "bob", HostName: "Bob"})
	_, _ = rooms.Create(ctx, feed.Room{HostID: "carol", HostName: "Carol"})

	list, err := rooms.ListByHosts(ctx, []string{"alice", "carol", "unknown-host"})
	if err != nil || len(list) != 2 {
		t.Fatalf("list by hosts: %+v err=%v", list, err)
	}
	found := false
	for _, r := range list {
		if r.RoomID == r1.RoomID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected alice's room in results: %+v", list)
	}
}

func TestPremiumUnlock_HasUnlockedAndRecordUnlock(t *testing.T) {
	rooms, unlocks, _ := newTestRepos(t)
	ctx := context.Background()
	room, _ := rooms.Create(ctx, feed.Room{HostID: "alice"})

	has, err := unlocks.HasUnlocked(ctx, room.RoomID, "viewer-1")
	if err != nil || has {
		t.Fatalf("should not be unlocked yet: %v err=%v", has, err)
	}
	if err := unlocks.RecordUnlock(ctx, room.RoomID, "viewer-1"); err != nil {
		t.Fatalf("record unlock: %v", err)
	}
	has2, err := unlocks.HasUnlocked(ctx, room.RoomID, "viewer-1")
	if err != nil || !has2 {
		t.Fatalf("should now be unlocked: %v err=%v", has2, err)
	}
	// Idempotent.
	if err := unlocks.RecordUnlock(ctx, room.RoomID, "viewer-1"); err != nil {
		t.Fatalf("re-record unlock: %v", err)
	}
}

func TestNotifications_CreateListMarkReadSummary(t *testing.T) {
	_, _, notifs := newTestRepos(t)
	ctx := context.Background()
	actor := "alice"

	if err := notifs.Create(ctx, feed.Notification{Type: "follow", ActorID: &actor, Body: "followed you"}); err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if err := notifs.Create(ctx, feed.Notification{Type: "gift", ActorID: &actor, Body: "sent a gift"}); err != nil {
		t.Fatalf("create 2: %v", err)
	}

	list, cursor, err := notifs.List(ctx, actor, "", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v cursor=%q err=%v", list, cursor, err)
	}
	if list[0].Body != "sent a gift" {
		t.Fatalf("expected newest-first ordering, got %+v", list)
	}

	summary, err := notifs.GetSummary(ctx, actor)
	if err != nil || summary.UnreadCount != 2 {
		t.Fatalf("summary: %+v err=%v", summary, err)
	}

	if err := notifs.MarkRead(ctx, actor, list[1].ID); err != nil { // mark up through the OLDER of the two
		t.Fatalf("mark read: %v", err)
	}
	summary2, err := notifs.GetSummary(ctx, actor)
	if err != nil || summary2.UnreadCount != 0 {
		t.Fatalf("expected 0 unread after marking through the oldest, got %+v err=%v", summary2, err)
	}
}
