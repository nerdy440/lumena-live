// Package social implements the social graph.
//
// Design decisions from doc 06 §4:
//   - Three independent tables (follows, blocks, mutes) — three independent axes.
//   - Unfollow is DELETE; it never touches blocks or mutes.
//   - Block never touches follows.
//   - Following list is read-your-own-writes consistent for the owner.
//   - Relationship is returned after every write so the client renders from truth.
package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ─── Domain types ─────────────────────────────────────────────────────────────

// Relationship is the complete relationship state between two accounts.
// Returned after every mutating operation (follow, unfollow, block, etc.)
// so the client always renders from the authoritative server value.
type Relationship struct {
	Following  bool `json:"following"`
	FollowedBy bool `json:"followed_by"`
	Blocked    bool `json:"blocked"`
	BlockedBy  bool `json:"blocked_by"`
	Muted      bool `json:"muted"`
	CanDM      bool `json:"can_dm"`
	CanCall    bool `json:"can_call"`
}

// FollowEntry is one item in a followers/following list.
type FollowEntry struct {
	AccountID   string    `json:"account_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
	FollowedAt  time.Time `json:"followed_at"`
}

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrSelfFollow  = errors.New("social: cannot follow yourself")
	ErrSelfBlock   = errors.New("social: cannot block yourself")
	ErrBlockedByOp = errors.New("social: cannot follow a user who has blocked you")
)

// ─── Repo interface ───────────────────────────────────────────────────────────

// Repo is the social-graph data-access contract.
type Repo interface {
	// Follow creates a follow relationship. Idempotent.
	// Returns the new authoritative Relationship.
	Follow(ctx context.Context, followerID, followeeID string) (*Relationship, error)

	// Unfollow removes a follow relationship. Idempotent.
	// NEVER touches the blocks or mutes tables.
	// Returns the new authoritative Relationship.
	Unfollow(ctx context.Context, followerID, followeeID string) (*Relationship, error)

	// Block creates a block. Idempotent.
	// NEVER touches the follows table.
	Block(ctx context.Context, blockerID, blockedID string) (*Relationship, error)

	// Unblock removes a block. Idempotent.
	Unblock(ctx context.Context, blockerID, blockedID string) (*Relationship, error)

	// Mute creates a one-way mute. Idempotent.
	Mute(ctx context.Context, muterID, mutedID string) (*Relationship, error)

	// Unmute removes a mute. Idempotent.
	Unmute(ctx context.Context, muterID, mutedID string) (*Relationship, error)

	// GetRelationship returns the relationship between two accounts.
	GetRelationship(ctx context.Context, viewerID, subjectID string) (*Relationship, error)

	// GetFollowing returns accounts that accountID follows.
	// ownerView: if true, always reads from the write path (read-your-own-writes, BT-03).
	GetFollowing(ctx context.Context, accountID string, ownerView bool, cursor string, limit int) ([]FollowEntry, string, error)

	// GetFollowers returns accounts that follow accountID.
	GetFollowers(ctx context.Context, accountID string, cursor string, limit int) ([]FollowEntry, string, error)

	// IsBlockedEitherWay returns true if either party has blocked the other.
	IsBlockedEitherWay(ctx context.Context, a, b string) (bool, error)
}

// ─── In-memory implementation ─────────────────────────────────────────────────

// followKey is a (follower, followee) pair.
type followKey struct{ follower, followee string }
type blockKey struct{ blocker, blocked string }
type muteKey struct{ muter, muted string }

// MemSocialRepo is the test/dev implementation.
type MemSocialRepo struct {
	mu      sync.RWMutex
	follows map[followKey]time.Time // key → created_at
	blocks  map[blockKey]time.Time
	mutes   map[muteKey]time.Time
	// displayNames is injected so following/follower lists can include the name
	displayNames map[string]string // accountID → displayName
}

func NewMemSocialRepo() *MemSocialRepo {
	return &MemSocialRepo{
		follows:      make(map[followKey]time.Time),
		blocks:       make(map[blockKey]time.Time),
		mutes:        make(map[muteKey]time.Time),
		displayNames: make(map[string]string),
	}
}

// RegisterDisplayName lets tests inject display names for the social repo.
func (r *MemSocialRepo) RegisterDisplayName(accountID, name string) {
	r.mu.Lock()
	r.displayNames[accountID] = name
	r.mu.Unlock()
}

// flatKey/blockEntry/muteEntry flatten the struct-keyed maps to a
// JSON-friendly shape — encoding/json can't marshal a map whose key is a
// struct.
type flatKey struct {
	A         string    `json:"a"`
	B         string    `json:"b"`
	CreatedAt time.Time `json:"created_at"`
}

type socialRepoSnapshot struct {
	Follows []flatKey `json:"follows"` // A=follower, B=followee
	Blocks  []flatKey `json:"blocks"`  // A=blocker, B=blocked
	Mutes   []flatKey `json:"mutes"`   // A=muter, B=muted
}

// Snapshot/Restore back the local-persistence-to-disk feature in
// feed/cmd/api. displayNames is deliberately NOT persisted — it's a cache
// populated from the profile service at startup/on-demand (see
// RegisterDisplayName's callers), not this repo's source of truth.
func (r *MemSocialRepo) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := socialRepoSnapshot{
		Follows: make([]flatKey, 0, len(r.follows)),
		Blocks:  make([]flatKey, 0, len(r.blocks)),
		Mutes:   make([]flatKey, 0, len(r.mutes)),
	}
	for k, t := range r.follows {
		s.Follows = append(s.Follows, flatKey{A: k.follower, B: k.followee, CreatedAt: t})
	}
	for k, t := range r.blocks {
		s.Blocks = append(s.Blocks, flatKey{A: k.blocker, B: k.blocked, CreatedAt: t})
	}
	for k, t := range r.mutes {
		s.Mutes = append(s.Mutes, flatKey{A: k.muter, B: k.muted, CreatedAt: t})
	}
	return json.Marshal(s)
}

// Restore replaces this repo's follow/block/mute state from a Snapshot's
// output. Only meaningful immediately after NewMemSocialRepo, before any
// traffic.
func (r *MemSocialRepo) Restore(data []byte) error {
	var s socialRepoSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.follows = make(map[followKey]time.Time, len(s.Follows))
	for _, k := range s.Follows {
		r.follows[followKey{follower: k.A, followee: k.B}] = k.CreatedAt
	}
	r.blocks = make(map[blockKey]time.Time, len(s.Blocks))
	for _, k := range s.Blocks {
		r.blocks[blockKey{blocker: k.A, blocked: k.B}] = k.CreatedAt
	}
	r.mutes = make(map[muteKey]time.Time, len(s.Mutes))
	for _, k := range s.Mutes {
		r.mutes[muteKey{muter: k.A, muted: k.B}] = k.CreatedAt
	}
	return nil
}

var _ Repo = (*MemSocialRepo)(nil)

func (r *MemSocialRepo) Follow(_ context.Context, followerID, followeeID string) (*Relationship, error) {
	if followerID == followeeID {
		return nil, ErrSelfFollow
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Block in either direction prevents follow — check this first.
	if r.isBlocked(followerID, followeeID) || r.isBlocked(followeeID, followerID) {
		return nil, ErrBlockedByOp
	}

	// Idempotent insert.
	k := followKey{followerID, followeeID}
	if _, exists := r.follows[k]; !exists {
		r.follows[k] = time.Now()
	}
	return r.relationship(followerID, followeeID), nil
}

func (r *MemSocialRepo) Unfollow(_ context.Context, followerID, followeeID string) (*Relationship, error) {
	if followerID == followeeID {
		return nil, ErrSelfFollow
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Delete — does NOT touch blocks or mutes.
	delete(r.follows, followKey{followerID, followeeID})
	return r.relationship(followerID, followeeID), nil
}

func (r *MemSocialRepo) Block(_ context.Context, blockerID, blockedID string) (*Relationship, error) {
	if blockerID == blockedID {
		return nil, ErrSelfBlock
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Block does NOT touch follows — the two axes are independent.
	r.blocks[blockKey{blockerID, blockedID}] = time.Now()
	return r.relationship(blockerID, blockedID), nil
}

func (r *MemSocialRepo) Unblock(_ context.Context, blockerID, blockedID string) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.blocks, blockKey{blockerID, blockedID})
	return r.relationship(blockerID, blockedID), nil
}

func (r *MemSocialRepo) Mute(_ context.Context, muterID, mutedID string) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutes[muteKey{muterID, mutedID}] = time.Now()
	return r.relationship(muterID, mutedID), nil
}

func (r *MemSocialRepo) Unmute(_ context.Context, muterID, mutedID string) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.mutes, muteKey{muterID, mutedID})
	return r.relationship(muterID, mutedID), nil
}

func (r *MemSocialRepo) GetRelationship(_ context.Context, viewerID, subjectID string) (*Relationship, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rel := r.relationship(viewerID, subjectID)
	return rel, nil
}

// GetFollowing returns accounts that accountID follows.
// ownerView=true forces a primary read (in this impl, always consistent anyway).
// This implements the BT-03 read-your-own-writes guarantee:
// a follow write is immediately visible in the owner's own following list.
func (r *MemSocialRepo) GetFollowing(_ context.Context, accountID string, _ bool, cursor string, limit int) ([]FollowEntry, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	type item struct {
		followeeID string
		at         time.Time
	}
	var items []item
	for k, t := range r.follows {
		if k.follower == accountID {
			items = append(items, item{k.followee, t})
		}
	}
	// Stable sort by time descending, then by ID for determinism
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].at.Before(items[j].at) ||
				(items[i].at.Equal(items[j].at) && items[i].followeeID > items[j].followeeID) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	// Cursor is the followeeID of the last seen item (unique, so exact even
	// when multiple follows share a timestamp — see TestGetFollowing_Pagination).
	start := 0
	if cursor != "" {
		for i, item := range items {
			if item.followeeID == cursor {
				start = i + 1
				break
			}
		}
	}
	items = items[start:]

	var entries []FollowEntry
	for _, item := range items {
		name := r.displayNames[item.followeeID]
		if name == "" {
			name = item.followeeID
		}
		entries = append(entries, FollowEntry{
			AccountID:   item.followeeID,
			DisplayName: name,
			FollowedAt:  item.at,
		})
		if len(entries) >= limit+1 {
			break
		}
	}

	var nextCursor string
	if len(entries) > limit {
		nextCursor = entries[limit-1].AccountID
		entries = entries[:limit]
	}
	return entries, nextCursor, nil
}

func (r *MemSocialRepo) GetFollowers(_ context.Context, accountID string, cursor string, limit int) ([]FollowEntry, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	type item struct {
		followerID string
		at         time.Time
	}
	var items []item
	for k, t := range r.follows {
		if k.followee == accountID {
			items = append(items, item{k.follower, t})
		}
	}
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].at.Before(items[j].at) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	start := 0
	if cursor != "" {
		for i, item := range items {
			if item.followerID == cursor {
				start = i + 1
				break
			}
		}
	}
	items = items[start:]

	var entries []FollowEntry
	for i, item := range items {
		if i >= limit+1 {
			break
		}
		name := r.displayNames[item.followerID]
		if name == "" {
			name = item.followerID
		}
		entries = append(entries, FollowEntry{
			AccountID:   item.followerID,
			DisplayName: name,
			FollowedAt:  item.at,
		})
	}

	var nextCursor string
	if len(entries) > limit {
		nextCursor = entries[limit].AccountID
		entries = entries[:limit]
	}
	return entries, nextCursor, nil
}

func (r *MemSocialRepo) IsBlockedEitherWay(_ context.Context, a, b string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isBlocked(a, b) || r.isBlocked(b, a), nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// relationship computes the Relationship between a and b.
// Caller must hold at least a read lock.
func (r *MemSocialRepo) relationship(a, b string) *Relationship {
	rel := &Relationship{
		Following:  r.follows[followKey{a, b}] != (time.Time{}),
		FollowedBy: r.follows[followKey{b, a}] != (time.Time{}),
		Blocked:    r.isBlocked(a, b),
		BlockedBy:  r.isBlocked(b, a),
		Muted:      r.mutes[muteKey{a, b}] != (time.Time{}),
	}
	canContact := !rel.Blocked && !rel.BlockedBy
	rel.CanDM = canContact
	rel.CanCall = canContact
	return rel
}

func (r *MemSocialRepo) isBlocked(blockerID, blockedID string) bool {
	_, ok := r.blocks[blockKey{blockerID, blockedID}]
	return ok
}

// ─── Postgres SQL (production reference) ────────────────────────────────────

// SQL statements for the Postgres implementation.
// The actual pgSocialRepo is in postgres/social_repo.go (integration build tag).

const sqlFollow = `
INSERT INTO follows (follower_id, followee_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING`

const sqlUnfollow = `
DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`

const sqlBlock = `
INSERT INTO blocks (blocker_id, blocked_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING`

const sqlUnblock = `
DELETE FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`

const sqlRelationship = `
SELECT
  EXISTS(SELECT 1 FROM follows WHERE follower_id=$1 AND followee_id=$2) AS following,
  EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$1) AS followed_by,
  EXISTS(SELECT 1 FROM blocks  WHERE blocker_id=$1  AND blocked_id=$2)  AS blocked,
  EXISTS(SELECT 1 FROM blocks  WHERE blocker_id=$2  AND blocked_id=$1)  AS blocked_by,
  EXISTS(SELECT 1 FROM mutes   WHERE muter_id=$1    AND muted_id=$2)    AS muted`

// sqlGetFollowing reads from the PRIMARY (not replica) when owner=true.
// This is the BT-03 implementation in Postgres: owner reads bypass the replica.
const sqlGetFollowing = `
SELECT f.followee_id, p.display_name, p.avatar_url, f.created_at
FROM follows f
JOIN profiles p ON p.account_id = f.followee_id
WHERE f.follower_id = $1 AND f.created_at < $2
ORDER BY f.created_at DESC
LIMIT $3`

const sqlIsBlockedEitherWay = `
SELECT EXISTS(
  SELECT 1 FROM blocks
  WHERE (blocker_id=$1 AND blocked_id=$2)
     OR (blocker_id=$2 AND blocked_id=$1)
)`

func init() {
	// Verify SQL constants are non-empty at startup
	for _, s := range []string{sqlFollow, sqlUnfollow, sqlBlock, sqlUnblock,
		sqlRelationship, sqlGetFollowing, sqlIsBlockedEitherWay} {
		if s == "" {
			panic("social: SQL constant is empty")
		}
	}
	fmt.Sprintf("%s", sqlFollow) // avoid unused warning
}
