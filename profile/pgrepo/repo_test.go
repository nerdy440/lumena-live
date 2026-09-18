package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/db"
	"github.com/lumena/profile"
	"github.com/lumena/profile/pgrepo"
)

func newTestRepos(t *testing.T) (*pgrepo.Repo, *pgrepo.PrivacyRepo, *pgxpool.Pool) {
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
	return pgrepo.New(pool), pgrepo.NewPrivacyRepo(pool), pool
}

func TestCreateProfile_IsIdempotent(t *testing.T) {
	r, _, _ := newTestRepos(t)
	ctx := context.Background()

	p1, err := r.Create(ctx, "acc-1", "Alice", "US")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p1.AccountID != "acc-1" || p1.DisplayName != "Alice" || p1.RegionCode != "US" || p1.Level != 1 {
		t.Fatalf("unexpected profile: %+v", p1)
	}

	p2, err := r.Create(ctx, "acc-1", "SomethingElse", "PK")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if p2.DisplayName != "Alice" {
		t.Fatalf("second create should return the existing row untouched, got %+v", p2)
	}
}

func TestGetProfile_NotFound(t *testing.T) {
	r, _, _ := newTestRepos(t)
	_, err := r.Get(context.Background(), "does-not-exist")
	if !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateProfile_HandleUniqueAndByHandleLookup(t *testing.T) {
	r, _, _ := newTestRepos(t)
	ctx := context.Background()
	_, _ = r.Create(ctx, "acc-1", "Alice", "US")
	_, _ = r.Create(ctx, "acc-2", "Bob", "US")

	handle := "alice_h"
	updated, err := r.Update(ctx, "acc-1", profile.UpdateRequest{Handle: &handle})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Handle == nil || *updated.Handle != handle {
		t.Fatalf("handle not set: %+v", updated)
	}

	byHandle, err := r.GetByHandle(ctx, "ALICE_H") // case-insensitive
	if err != nil || byHandle.AccountID != "acc-1" {
		t.Fatalf("GetByHandle: %+v err=%v", byHandle, err)
	}

	// Bob can't take the same handle.
	_, err = r.Update(ctx, "acc-2", profile.UpdateRequest{Handle: &handle})
	if !errors.Is(err, profile.ErrHandleTaken) {
		t.Fatalf("expected ErrHandleTaken, got %v", err)
	}
}

func TestUpdateProfile_AvatarURLResetsToPending(t *testing.T) {
	r, _, _ := newTestRepos(t)
	ctx := context.Background()
	_, _ = r.Create(ctx, "acc-1", "Alice", "US")

	url := "https://example.com/a.jpg"
	updated, err := r.Update(ctx, "acc-1", profile.UpdateRequest{AvatarURL: &url})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.AvatarURL == nil || *updated.AvatarURL != url {
		t.Fatalf("avatar url not set: %+v", updated)
	}
	if updated.AvatarState != profile.AvatarPending {
		t.Fatalf("avatar state = %q, want pending", updated.AvatarState)
	}
}

func TestGetCounts_ReflectsRealFollowRows(t *testing.T) {
	r, _, pool := newTestRepos(t)
	ctx := context.Background()
	_, _ = r.Create(ctx, "acc-1", "Alice", "US")
	_, _ = r.Create(ctx, "acc-2", "Bob", "US")
	_, _ = r.Create(ctx, "acc-3", "Carol", "US")

	if _, err := pool.Exec(ctx, `INSERT INTO follows (follower_id, followee_id) VALUES ($1,$2),($3,$2)`,
		"acc-1", "acc-2", "acc-3"); err != nil {
		t.Fatalf("seed follows: %v", err)
	}

	counts, err := r.GetCounts(ctx, "acc-2")
	if err != nil {
		t.Fatalf("GetCounts: %v", err)
	}
	if counts.FollowerCount != 2 {
		t.Fatalf("follower count = %d, want 2", counts.FollowerCount)
	}

	// An account nobody follows and who follows nobody still reads zero,
	// not an error — matches MemProfileRepo.
	zero, err := r.GetCounts(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetCounts: %v", err)
	}
	if zero.FollowerCount != 0 || zero.FollowingCount != 1 {
		t.Fatalf("unexpected counts for acc-1: %+v", zero)
	}
}

func TestSearch_MatchesNameOrHandleCaseInsensitive(t *testing.T) {
	r, _, _ := newTestRepos(t)
	ctx := context.Background()
	_, _ = r.Create(ctx, "acc-1", "Aisha Streams", "US")
	_, _ = r.Create(ctx, "acc-2", "Bob Beats", "US")
	handle := "aisha_official"
	_, _ = r.Update(ctx, "acc-2", profile.UpdateRequest{Handle: &handle})

	results, _, err := r.Search(ctx, "aisha", 10, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 matches (name + handle), got %d: %+v", len(results), results)
	}
}

func TestPrivacyRepo_DefaultsThenUpdate(t *testing.T) {
	r, priv, _ := newTestRepos(t)
	ctx := context.Background()

	def, err := priv.Get(ctx, "acc-nobody")
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	want := profile.DefaultPrivacySettings("acc-nobody")
	if *def != want {
		t.Fatalf("default privacy = %+v, want %+v", *def, want)
	}

	// privacy_settings.account_id FK-references profiles(account_id) — a
	// real Update needs the owning profile row to exist first, unlike the
	// in-memory repo which has no such constraint.
	if _, err := r.Create(ctx, "acc-nobody", "Nobody", "US"); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	updated, err := priv.Update(ctx, "acc-nobody", profile.PrivacySettings{
		WhoCanDM: profile.PolicyNobody, WhoCanCall: profile.PolicyEveryone,
		ShowPresence: false, Discoverable: false, Matchable: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := priv.Get(ctx, "acc-nobody")
	if err != nil || *again != *updated {
		t.Fatalf("get after update = %+v, want %+v (err=%v)", again, updated, err)
	}
}
