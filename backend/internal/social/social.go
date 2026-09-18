// Package social implements the social graph module.
// Key design decisions:
//   - Follow and block are three independent tables, three independent axes.
//   - Unfollow is DELETE FROM follows — a first-class inverse, not a block.
//   - Follow writes are read-your-own-writes consistent for the follower.
//   - Block never touches the follows table.
package social

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Relationship is the authoritative relationship object returned to clients.
// The client renders from this — never from an optimistic guess.
type Relationship struct {
	Following  bool `json:"following"`
	FollowedBy bool `json:"followed_by"`
	Blocked    bool `json:"blocked"`
	BlockedBy  bool `json:"blocked_by"`
	Muted      bool `json:"muted"`
	// Derived permissions
	CanDM   bool `json:"can_dm"`
	CanCall bool `json:"can_call"`
}

// FollowEntry is one item in a following/followers list.
type FollowEntry struct {
	AccountID   uuid.UUID `json:"account_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
	FollowedAt  time.Time `json:"followed_at"`
}

// Service handles the social graph.
type Service struct {
	// primary is the read-write primary. Used for owner reads (BT-03).
	primary *pgxpool.Pool
	// replica is for non-owner reads (follower counts, other users' lists).
	// If nil, primary is used for all reads.
	replica *pgxpool.Pool
}

func NewService(primary, replica *pgxpool.Pool) *Service {
	if replica == nil {
		replica = primary
	}
	return &Service{primary: primary, replica: replica}
}

// Follow makes followerID follow followeeID.
// Idempotent: following twice returns the current relationship, no error.
// Does NOT touch blocks or mutes.
func (s *Service) Follow(ctx context.Context, followerID, followeeID uuid.UUID) (*Relationship, error) {
	if followerID == followeeID {
		return nil, fmt.Errorf("cannot follow yourself")
	}

	// Check for block in either direction before allowing the follow.
	blocked, err := s.isBlockedEitherWay(ctx, followerID, followeeID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, fmt.Errorf("blocked: cannot follow")
	}

	_, err = s.primary.Exec(ctx, `
		INSERT INTO follows (follower_id, followee_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, followerID, followeeID)
	if err != nil {
		return nil, fmt.Errorf("social: follow: %w", err)
	}

	// Update denormalized counts (best-effort, not in the same tx — counts may lag, membership never does)
	_, _ = s.primary.Exec(ctx, `
		INSERT INTO follow_counts (account_id, following_count) VALUES ($1, 1)
		ON CONFLICT (account_id) DO UPDATE SET following_count = follow_counts.following_count + 1, updated_at = now()
	`, followerID)
	_, _ = s.primary.Exec(ctx, `
		INSERT INTO follow_counts (account_id, follower_count) VALUES ($1, 1)
		ON CONFLICT (account_id) DO UPDATE SET follower_count = follow_counts.follower_count + 1, updated_at = now()
	`, followeeID)

	// Return authoritative relationship from primary (BT-03).
	return s.getRelationship(ctx, s.primary, followerID, followeeID)
}

// Unfollow removes followerID's follow of followeeID.
// Idempotent: unfollowing when not following returns the current relationship, no error.
// DOES NOT touch blocks or mutes — these are independent.
func (s *Service) Unfollow(ctx context.Context, followerID, followeeID uuid.UUID) (*Relationship, error) {
	if followerID == followeeID {
		return nil, fmt.Errorf("cannot unfollow yourself")
	}

	tag, err := s.primary.Exec(ctx, `
		DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2
	`, followerID, followeeID)
	if err != nil {
		return nil, fmt.Errorf("social: unfollow: %w", err)
	}

	if tag.RowsAffected() > 0 {
		_, _ = s.primary.Exec(ctx, `
			UPDATE follow_counts SET following_count = GREATEST(0, following_count - 1), updated_at = now()
			WHERE account_id = $1
		`, followerID)
		_, _ = s.primary.Exec(ctx, `
			UPDATE follow_counts SET follower_count = GREATEST(0, follower_count - 1), updated_at = now()
			WHERE account_id = $1
		`, followeeID)
	}

	// Return authoritative relationship from primary.
	return s.getRelationship(ctx, s.primary, followerID, followeeID)
}

// Block makes blockerID block blockedID.
// Orthogonal to follow: block does NOT unfollow either party.
// The application layer removes feed entries for blocked users at read time.
func (s *Service) Block(ctx context.Context, blockerID, blockedID uuid.UUID) (*Relationship, error) {
	if blockerID == blockedID {
		return nil, fmt.Errorf("cannot block yourself")
	}

	_, err := s.primary.Exec(ctx, `
		INSERT INTO blocks (blocker_id, blocked_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, blockerID, blockedID)
	if err != nil {
		return nil, fmt.Errorf("social: block: %w", err)
	}

	return s.getRelationship(ctx, s.primary, blockerID, blockedID)
}

// Unblock removes blockerID's block of blockedID.
func (s *Service) Unblock(ctx context.Context, blockerID, blockedID uuid.UUID) (*Relationship, error) {
	_, err := s.primary.Exec(ctx, `
		DELETE FROM blocks WHERE blocker_id = $1 AND blocked_id = $2
	`, blockerID, blockedID)
	if err != nil {
		return nil, fmt.Errorf("social: unblock: %w", err)
	}
	return s.getRelationship(ctx, s.primary, blockerID, blockedID)
}

// GetRelationship returns the current relationship between two accounts.
// selfView: use the primary (for the account owner — BT-03).
// otherView: may use replica (counts can lag; relationship cannot for the owner).
func (s *Service) GetRelationship(ctx context.Context, requesterID, targetID uuid.UUID) (*Relationship, error) {
	// Owner always reads from primary.
	return s.getRelationship(ctx, s.primary, requesterID, targetID)
}

// GetFollowing returns the list of accounts that accountID follows.
// For the account owner, always served from primary (BT-03: read-your-own-writes).
func (s *Service) GetFollowing(ctx context.Context, accountID, requesterID uuid.UUID, cursor string, limit int) ([]FollowEntry, string, error) {
	pool := s.replica
	if accountID == requesterID {
		pool = s.primary // owner reads from primary
	}

	var cursorTime time.Time
	if cursor != "" {
		if err := cursorTime.UnmarshalText([]byte(cursor)); err != nil {
			cursorTime = time.Now()
		}
	} else {
		cursorTime = time.Now()
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := pool.Query(ctx, `
		SELECT f.followee_id, p.display_name, p.avatar_url, f.created_at
		FROM follows f
		JOIN profiles p ON p.account_id = f.followee_id
		WHERE f.follower_id = $1 AND f.created_at < $2
		ORDER BY f.created_at DESC
		LIMIT $3
	`, accountID, cursorTime, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("social: get following: %w", err)
	}
	defer rows.Close()

	var entries []FollowEntry
	for rows.Next() {
		var e FollowEntry
		if err := rows.Scan(&e.AccountID, &e.DisplayName, &e.AvatarURL, &e.FollowedAt); err != nil {
			return nil, "", err
		}
		entries = append(entries, e)
	}

	var nextCursor string
	if len(entries) > limit {
		nextCursor, _ = entries[limit-1].FollowedAt.MarshalText()
		entries = entries[:limit]
	}

	return entries, string(nextCursor), nil
}

// ─────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────

func (s *Service) getRelationship(ctx context.Context, pool *pgxpool.Pool, a, b uuid.UUID) (*Relationship, error) {
	var rel Relationship

	err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM follows WHERE follower_id=$1 AND followee_id=$2) AS following,
		  EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$1) AS followed_by,
		  EXISTS(SELECT 1 FROM blocks WHERE blocker_id=$1 AND blocked_id=$2)   AS blocked,
		  EXISTS(SELECT 1 FROM blocks WHERE blocker_id=$2 AND blocked_id=$1)   AS blocked_by,
		  EXISTS(SELECT 1 FROM mutes  WHERE muter_id=$1   AND muted_id=$2)     AS muted
	`, a, b).Scan(
		&rel.Following, &rel.FollowedBy,
		&rel.Blocked, &rel.BlockedBy,
		&rel.Muted,
	)
	if err != nil {
		return nil, fmt.Errorf("social: get relationship: %w", err)
	}

	// Derive permissions (conservative: block in either direction prevents DM/call)
	canContact := !rel.Blocked && !rel.BlockedBy
	rel.CanDM = canContact   // privacy settings checked at call site
	rel.CanCall = canContact // privacy settings checked at call site

	return &rel, nil
}

func (s *Service) isBlockedEitherWay(ctx context.Context, a, b uuid.UUID) (bool, error) {
	var blocked bool
	err := s.primary.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM blocks WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1)
		)
	`, a, b).Scan(&blocked)
	return blocked, err
}
