package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/lumena/db"
	profilepg "github.com/lumena/profile/pgrepo"
	"github.com/lumena/profile/social"
	"github.com/lumena/profile/social/pgrepo"
)

func newTestRepo(t *testing.T) (*pgrepo.Repo, *profilepg.Repo) {
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
	if err := db.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.New(pool), profilepg.New(pool)
}

func seedProfiles(t *testing.T, pr *profilepg.Repo, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := pr.Create(context.Background(), id, id, "US"); err != nil {
			t.Fatalf("seed profile %s: %v", id, err)
		}
	}
}

func TestFollow_IdempotentAndSelfFollowRejected(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob")

	if _, err := r.Follow(ctx, "alice", "alice"); !errors.Is(err, social.ErrSelfFollow) {
		t.Fatalf("expected ErrSelfFollow, got %v", err)
	}

	rel1, err := r.Follow(ctx, "alice", "bob")
	if err != nil || !rel1.Following {
		t.Fatalf("follow: rel=%+v err=%v", rel1, err)
	}
	rel2, err := r.Follow(ctx, "alice", "bob") // idempotent retry
	if err != nil || !rel2.Following {
		t.Fatalf("second follow: rel=%+v err=%v", rel2, err)
	}
}

func TestUnfollow_NeverTouchesBlocksOrMutes(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob")

	_, _ = r.Follow(ctx, "alice", "bob")
	_, _ = r.Mute(ctx, "alice", "bob")

	rel, err := r.Unfollow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if rel.Following {
		t.Fatalf("still following after unfollow: %+v", rel)
	}
	if !rel.Muted {
		t.Fatalf("unfollow must not clear mute: %+v", rel)
	}
}

func TestBlock_NeverTouchesFollowsAndBlocksBothDirectionsVisible(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob")

	_, _ = r.Follow(ctx, "alice", "bob")
	rel, err := r.Block(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("block: %v", err)
	}
	if !rel.Blocked {
		t.Fatalf("blocked not set: %+v", rel)
	}
	if !rel.Following {
		t.Fatalf("block must not touch follows: %+v", rel)
	}

	fromBobSide, err := r.GetRelationship(ctx, "bob", "alice")
	if err != nil || !fromBobSide.BlockedBy {
		t.Fatalf("bob should see blocked_by=true: %+v err=%v", fromBobSide, err)
	}
	if fromBobSide.CanDM || fromBobSide.CanCall {
		t.Fatalf("blocked relationship must disable contact: %+v", fromBobSide)
	}

	blockedEitherWay, err := r.IsBlockedEitherWay(ctx, "bob", "alice")
	if err != nil || !blockedEitherWay {
		t.Fatalf("IsBlockedEitherWay = %v, err=%v, want true", blockedEitherWay, err)
	}
}

func TestFollow_BlockedEitherDirectionRejectsFollow(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob")

	_, _ = r.Block(ctx, "bob", "alice")
	if _, err := r.Follow(ctx, "alice", "bob"); !errors.Is(err, social.ErrBlockedByOp) {
		t.Fatalf("expected ErrBlockedByOp, got %v", err)
	}
}

func TestGetFollowing_And_GetFollowers_Pagination(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob", "carol", "dave")

	_, _ = r.Follow(ctx, "alice", "bob")
	_, _ = r.Follow(ctx, "alice", "carol")
	_, _ = r.Follow(ctx, "alice", "dave")

	page1, cursor, err := r.GetFollowing(ctx, "alice", true, "", 2)
	if err != nil {
		t.Fatalf("GetFollowing page1: %v", err)
	}
	if len(page1) != 2 || cursor == "" {
		t.Fatalf("expected 2 items + cursor, got %d items cursor=%q", len(page1), cursor)
	}
	page2, cursor2, err := r.GetFollowing(ctx, "alice", true, cursor, 2)
	if err != nil {
		t.Fatalf("GetFollowing page2: %v", err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("expected final page of 1 with no cursor, got %d items cursor=%q", len(page2), cursor2)
	}

	followers, _, err := r.GetFollowers(ctx, "bob", "", 10)
	if err != nil {
		t.Fatalf("GetFollowers: %v", err)
	}
	if len(followers) != 1 || followers[0].AccountID != "alice" {
		t.Fatalf("unexpected followers of bob: %+v", followers)
	}
}

func TestGetBlocked_ListsOnlyOwnBlocksAndPaginates(t *testing.T) {
	r, pr := newTestRepo(t)
	ctx := context.Background()
	seedProfiles(t, pr, "alice", "bob", "x1", "x2", "x3")

	for _, id := range []string{"x1", "x2", "x3"} {
		if _, err := r.Block(ctx, "alice", id); err != nil {
			t.Fatalf("block %s: %v", id, err)
		}
	}
	if _, err := r.Block(ctx, "bob", "x1"); err != nil {
		t.Fatalf("bob block: %v", err)
	}

	page1, cursor, err := r.GetBlocked(ctx, "alice", "", 2)
	if err != nil {
		t.Fatalf("GetBlocked page1: %v", err)
	}
	if len(page1) != 2 || cursor == "" {
		t.Fatalf("expected 2 items + cursor, got %d items cursor=%q", len(page1), cursor)
	}
	page2, cursor2, err := r.GetBlocked(ctx, "alice", cursor, 2)
	if err != nil {
		t.Fatalf("GetBlocked page2: %v", err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("expected final page of 1 with no cursor, got %d items cursor=%q", len(page2), cursor2)
	}

	bobList, _, err := r.GetBlocked(ctx, "bob", "", 10)
	if err != nil {
		t.Fatalf("bob's GetBlocked: %v", err)
	}
	if len(bobList) != 1 || bobList[0].AccountID != "x1" {
		t.Fatalf("bob's block list should only contain x1, got %+v", bobList)
	}
}
