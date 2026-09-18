package social_test

import (
	"context"
	"testing"

	"github.com/lumena/profile/social"
)

func newRepo() social.Repo {
	return social.NewMemSocialRepo()
}

// ─── Follow / Unfollow (C4 fix) ───────────────────────────────────────────────

// TestUnfollow_IsFirstClassInverse verifies that unfollow is a plain inverse of follow
// and NEVER requires blocking (the C4 defect on the reference product).
func TestUnfollow_IsFirstClassInverse(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	// Follow
	rel, err := repo.Follow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	if !rel.Following {
		t.Error("expected following=true after Follow")
	}

	// Unfollow — must work without any block operation
	rel, err = repo.Unfollow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if rel.Following {
		t.Error("expected following=false after Unfollow")
	}

	// Block state must be untouched
	if rel.Blocked || rel.BlockedBy {
		t.Error("unfollow must not create a block (C4: unfollow ≠ block)")
	}
}

// TestUnfollow_DoesNotRequireBlock specifically tests the C4 defect scenario:
// on the reference product, users had to block to unfollow. This must be impossible here.
func TestUnfollow_DoesNotRequireBlock(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	_, _ = repo.Follow(ctx, "alice", "bob")

	// Unfollow directly — no block involved
	rel, err := repo.Unfollow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("unfollow without block: %v", err)
	}

	// alice does not follow bob
	if rel.Following {
		t.Error("C4: unfollow must clear following=true without requiring a block")
	}
	// alice has NOT blocked bob
	if rel.Blocked {
		t.Error("C4: unfollow must not set blocked=true")
	}
	// alice can still re-follow bob (block didn't happen)
	rel2, err := repo.Follow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("re-follow after unfollow: %v", err)
	}
	if !rel2.Following {
		t.Error("C4: should be able to re-follow after unfollow without any block in the way")
	}
}

// ─── Follow list consistency (C5 fix) ─────────────────────────────────────────

// TestFollowing_ReadYourOwnWrites verifies that a follow is immediately
// visible in the owner's own following list — the C5 defect on the reference product
// where followed users didn't appear in the following tab.
func TestFollowing_ReadYourOwnWrites(t *testing.T) {
	r := social.NewMemSocialRepo()
	r.RegisterDisplayName("bob", "Bob")
	r.RegisterDisplayName("carol", "Carol")
	ctx := context.Background()

	// Alice follows bob
	_, err := r.Follow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("follow bob: %v", err)
	}

	// Immediately read alice's following list (owner view = true → primary read)
	entries, _, err := r.GetFollowing(ctx, "alice", true, "", 50)
	if err != nil {
		t.Fatalf("get following: %v", err)
	}

	// Bob must be present immediately — this is the C5 fix
	found := false
	for _, e := range entries {
		if e.AccountID == "bob" {
			found = true
			break
		}
	}
	if !found {
		t.Error("C5: bob must appear in alice's following list immediately after Follow")
	}
}

// TestFollowing_UnfollowRemovedImmediately verifies that unfollow is also immediately
// reflected in the owner's following list.
func TestFollowing_UnfollowRemovedImmediately(t *testing.T) {
	r := social.NewMemSocialRepo()
	r.RegisterDisplayName("bob", "Bob")
	ctx := context.Background()

	_, _ = r.Follow(ctx, "alice", "bob")
	_, _ = r.Unfollow(ctx, "alice", "bob")

	// Immediately verify bob is gone
	entries, _, err := r.GetFollowing(ctx, "alice", true, "", 50)
	if err != nil {
		t.Fatalf("get following: %v", err)
	}
	for _, e := range entries {
		if e.AccountID == "bob" {
			t.Error("C5: bob must be removed from following list immediately after Unfollow")
		}
	}
}

// ─── Block independence ───────────────────────────────────────────────────────

// TestBlock_IndependentOfFollow verifies that block and follow are orthogonal.
// Blocking does not unfollow. Unfollowing does not block.
func TestBlock_IndependentOfFollow(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	// Alice follows bob, then blocks carol
	_, _ = repo.Follow(ctx, "alice", "bob")
	_, err := repo.Block(ctx, "alice", "carol")
	if err != nil {
		t.Fatalf("block carol: %v", err)
	}

	// Alice still follows bob (block of carol didn't affect bob-follow)
	relBob, _ := repo.GetRelationship(ctx, "alice", "bob")
	if !relBob.Following {
		t.Error("block must not affect unrelated follow relationships")
	}

	// Alice is not blocked by / blocking bob
	if relBob.Blocked || relBob.BlockedBy {
		t.Error("block of carol must not contaminate relationship with bob")
	}

	// Alice has blocked carol but NOT unfollowed carol (if she was following)
	relCarol, _ := repo.GetRelationship(ctx, "alice", "carol")
	if !relCarol.Blocked {
		t.Error("expected blocked=true for carol")
	}
}

// TestBlock_DoesNotTouchFollows verifies block never writes the follows table.
func TestBlock_DoesNotTouchFollows(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	// Alice follows bob, then bob blocks alice
	_, _ = repo.Follow(ctx, "alice", "bob")
	_, _ = repo.Block(ctx, "bob", "alice")

	// The follow row still exists (block didn't delete it)
	// But alice cannot add a new follow (blocked)
	_, err := repo.Follow(ctx, "alice", "bob")
	if err == nil {
		t.Error("expected error when trying to follow a user who blocked you")
	}

	// Bob can unblock alice; alice can re-follow after
	_, _ = repo.Unblock(ctx, "bob", "alice")
	_, err = repo.Follow(ctx, "alice", "bob")
	if err != nil {
		t.Errorf("should be able to follow after unblock: %v", err)
	}
}

// ─── Idempotency ─────────────────────────────────────────────────────────────

func TestFollow_Idempotent(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	rel1, err1 := repo.Follow(ctx, "alice", "bob")
	rel2, err2 := repo.Follow(ctx, "alice", "bob")

	if err1 != nil || err2 != nil {
		t.Fatalf("double follow: %v / %v", err1, err2)
	}
	if !rel1.Following || !rel2.Following {
		t.Error("both results must show following=true")
	}
}

func TestUnfollow_Idempotent(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	rel1, err1 := repo.Unfollow(ctx, "alice", "bob") // never followed
	rel2, err2 := repo.Unfollow(ctx, "alice", "bob") // also never followed

	if err1 != nil || err2 != nil {
		t.Fatalf("double unfollow: %v / %v", err1, err2)
	}
	if rel1.Following || rel2.Following {
		t.Error("both results must show following=false")
	}
}

func TestBlock_Idempotent(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	_, err1 := repo.Block(ctx, "alice", "bob")
	_, err2 := repo.Block(ctx, "alice", "bob")
	if err1 != nil || err2 != nil {
		t.Fatalf("double block: %v / %v", err1, err2)
	}
}

// ─── Self-operation prevention ────────────────────────────────────────────────

func TestFollow_Self_Error(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()
	_, err := repo.Follow(ctx, "alice", "alice")
	if err == nil {
		t.Error("expected error for self-follow")
	}
}

func TestBlock_Self_Error(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()
	_, err := repo.Block(ctx, "alice", "alice")
	if err == nil {
		t.Error("expected error for self-block")
	}
}

// ─── Relationship completeness ────────────────────────────────────────────────

func TestRelationship_AllFields(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	// alice follows bob; bob doesn't follow alice; alice mutes carol
	_, _ = repo.Follow(ctx, "alice", "bob")
	_, _ = repo.Mute(ctx, "alice", "carol")

	relAliceBob, _ := repo.GetRelationship(ctx, "alice", "bob")
	if !relAliceBob.Following {
		t.Error("alice→bob: following must be true")
	}
	if relAliceBob.FollowedBy {
		t.Error("alice→bob: followed_by must be false (bob didn't follow back)")
	}
	if relAliceBob.Blocked || relAliceBob.BlockedBy {
		t.Error("alice→bob: no block in either direction")
	}
	if !relAliceBob.CanDM || !relAliceBob.CanCall {
		t.Error("alice→bob: can_dm and can_call should be true (no block)")
	}

	// Mutual follow
	_, _ = repo.Follow(ctx, "bob", "alice")
	relBobAlice, _ := repo.GetRelationship(ctx, "bob", "alice")
	if !relBobAlice.Following || !relBobAlice.FollowedBy {
		t.Error("mutual follow: both following and followed_by must be true")
	}

	// Block disables contact permissions
	_, _ = repo.Block(ctx, "alice", "bob")
	relAfterBlock, _ := repo.GetRelationship(ctx, "alice", "bob")
	if relAfterBlock.CanDM || relAfterBlock.CanCall {
		t.Error("after block: can_dm and can_call must be false")
	}
}

// ─── Mutual follows ───────────────────────────────────────────────────────────

func TestMutualFollow(t *testing.T) {
	repo := newRepo()
	ctx := context.Background()

	_, _ = repo.Follow(ctx, "alice", "bob")
	_, _ = repo.Follow(ctx, "bob", "alice")

	rel, _ := repo.GetRelationship(ctx, "alice", "bob")
	if !rel.Following || !rel.FollowedBy {
		t.Errorf("mutual follow: following=%v followed_by=%v (both must be true)", rel.Following, rel.FollowedBy)
	}
}

// ─── Pagination ───────────────────────────────────────────────────────────────

func TestGetFollowing_Pagination(t *testing.T) {
	r := social.NewMemSocialRepo()
	ctx := context.Background()

	// Alice follows 5 people
	followees := []string{"b1", "b2", "b3", "b4", "b5"}
	for _, id := range followees {
		r.RegisterDisplayName(id, "User "+id)
		_, _ = r.Follow(ctx, "alice", id)
	}

	// Page 1: limit 3
	page1, cursor, err := r.GetFollowing(ctx, "alice", true, "", 3)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("page 1: expected 3 items, got %d", len(page1))
	}
	if cursor == "" {
		t.Error("page 1: expected a next cursor")
	}

	// Page 2: remaining items
	page2, cursor2, err := r.GetFollowing(ctx, "alice", true, cursor, 3)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2: expected 2 items, got %d", len(page2))
	}
	if cursor2 != "" {
		t.Error("page 2: expected no next cursor (end of list)")
	}

	// No duplicates across pages
	seen := make(map[string]bool)
	for _, e := range append(page1, page2...) {
		if seen[e.AccountID] {
			t.Errorf("duplicate account %s across pages", e.AccountID)
		}
		seen[e.AccountID] = true
	}
	if len(seen) != 5 {
		t.Errorf("expected 5 unique accounts, got %d", len(seen))
	}
}

func TestGetBlocked_ListsOnlyOwnBlocksAndPaginates(t *testing.T) {
	r := social.NewMemSocialRepo()
	ctx := context.Background()

	for _, id := range []string{"x1", "x2", "x3"} {
		r.RegisterDisplayName(id, "User "+id)
		if _, err := r.Block(ctx, "alice", id); err != nil {
			t.Fatalf("block %s: %v", id, err)
		}
	}
	// A block by someone else must never show up in alice's list.
	if _, err := r.Block(ctx, "bob", "x1"); err != nil {
		t.Fatalf("bob block: %v", err)
	}

	page1, cursor, err := r.GetBlocked(ctx, "alice", "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page 1: expected 2 items, got %d", len(page1))
	}
	if cursor == "" {
		t.Error("page 1: expected a next cursor")
	}

	page2, cursor2, err := r.GetBlocked(ctx, "alice", cursor, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 {
		t.Errorf("page 2: expected 1 item, got %d", len(page2))
	}
	if cursor2 != "" {
		t.Error("page 2: expected no next cursor (end of list)")
	}

	bobList, _, err := r.GetBlocked(ctx, "bob", "", 10)
	if err != nil {
		t.Fatalf("bob list: %v", err)
	}
	if len(bobList) != 1 || bobList[0].AccountID != "x1" {
		t.Errorf("bob's block list should only contain x1, got %+v", bobList)
	}
}
