package pgrepo_test

import (
	"context"
	"os"
	"testing"

	"github.com/lumena/db"
	"github.com/lumena/streaming"
	"github.com/lumena/streaming/pgrepo"
)

func newTestRepo(t *testing.T) *pgrepo.Repo {
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
	return pgrepo.New(pool)
}

func TestSession_CreateGetGetByRoom(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	s, err := repo.Create(ctx, streaming.StreamSession{RoomID: "room-1", HostID: "alice", State: streaming.StateScheduled})
	if err != nil || s.ID == "" {
		t.Fatalf("create: %+v err=%v", s, err)
	}

	got, err := repo.Get(ctx, s.ID)
	if err != nil || got.RoomID != "room-1" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	byRoom, err := repo.GetByRoom(ctx, "room-1")
	if err != nil || byRoom.ID != s.ID {
		t.Fatalf("get by room: %+v err=%v", byRoom, err)
	}

	if _, err := repo.Get(ctx, "nonexistent"); err != streaming.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSession_UpdateStateSetsStartedAndEndedAt(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s, _ := repo.Create(ctx, streaming.StreamSession{RoomID: "room-2", HostID: "bob", State: streaming.StateScheduled})

	if err := repo.UpdateState(ctx, s.ID, streaming.StateLive, ""); err != nil {
		t.Fatalf("update state to live: %v", err)
	}
	live, _ := repo.Get(ctx, s.ID)
	if live.State != streaming.StateLive || live.StartedAt == nil {
		t.Fatalf("expected live state with StartedAt set: %+v", live)
	}

	if err := repo.UpdateState(ctx, s.ID, streaming.StateEnded, "host ended"); err != nil {
		t.Fatalf("update state to ended: %v", err)
	}
	ended, _ := repo.Get(ctx, s.ID)
	if ended.State != streaming.StateEnded || ended.EndedAt == nil || ended.EndReason != "host ended" {
		t.Fatalf("expected ended state with EndedAt/EndReason set: %+v", ended)
	}
}

func TestSession_GetActiveOnlyReturnsLiveAndReconnecting(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	live, _ := repo.Create(ctx, streaming.StreamSession{RoomID: "room-3", HostID: "carol", State: streaming.StateScheduled})
	_ = repo.UpdateState(ctx, live.ID, streaming.StateLive, "")
	_, _ = repo.Create(ctx, streaming.StreamSession{RoomID: "room-4", HostID: "dave", State: streaming.StateScheduled})

	active, err := repo.GetActive(ctx)
	if err != nil || len(active) != 1 || active[0].ID != live.ID {
		t.Fatalf("get active: %+v err=%v", active, err)
	}
}

func TestSession_UpdateHealthAndPlaybackURL(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s, _ := repo.Create(ctx, streaming.StreamSession{RoomID: "room-5", HostID: "erin", State: streaming.StateLive})

	h := streaming.StreamHealth{BitrateKbps: 4500, FPS: 30, Quality: "good"}
	if err := repo.UpdateHealth(ctx, s.ID, h); err != nil {
		t.Fatalf("update health: %v", err)
	}
	got, _ := repo.Get(ctx, s.ID)
	if got.Health == nil || got.Health.BitrateKbps != 4500 || got.Health.Quality != "good" {
		t.Fatalf("health not persisted: %+v", got.Health)
	}

	if err := repo.UpdatePlaybackURL(ctx, s.ID, "https://cdn.example.com/room-5/index.m3u8"); err != nil {
		t.Fatalf("update playback url: %v", err)
	}
	got2, _ := repo.Get(ctx, s.ID)
	if got2.PlaybackURL != "https://cdn.example.com/room-5/index.m3u8" {
		t.Fatalf("playback url not persisted: %+v", got2)
	}
}

func TestSession_ListByHostAndListAll(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	_, _ = repo.Create(ctx, streaming.StreamSession{RoomID: "room-6", HostID: "frank", State: streaming.StateScheduled})
	_, _ = repo.Create(ctx, streaming.StreamSession{RoomID: "room-7", HostID: "frank", State: streaming.StateScheduled})
	_, _ = repo.Create(ctx, streaming.StreamSession{RoomID: "room-8", HostID: "gina", State: streaming.StateScheduled})

	byHost, err := repo.ListByHost(ctx, "frank")
	if err != nil || len(byHost) != 2 {
		t.Fatalf("list by host: %+v err=%v", byHost, err)
	}

	all, err := repo.ListAll(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: %+v err=%v", all, err)
	}
}

func TestSession_End(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s, _ := repo.Create(ctx, streaming.StreamSession{RoomID: "room-9", HostID: "hank", State: streaming.StateLive})

	if err := repo.End(ctx, s.ID, "network drop"); err != nil {
		t.Fatalf("end: %v", err)
	}
	got, _ := repo.Get(ctx, s.ID)
	if got.State != streaming.StateEnded || got.EndReason != "network drop" {
		t.Fatalf("end not persisted correctly: %+v", got)
	}

	if err := repo.End(ctx, "nonexistent", "x"); err != streaming.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound ending a missing session, got %v", err)
	}
}
