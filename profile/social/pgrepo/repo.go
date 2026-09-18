// Package pgrepo implements social.Repo against real Postgres — the
// production counterpart to social.MemSocialRepo, matching its semantics
// exactly: follow/unfollow/block/unblock/mute/unmute are all idempotent,
// unfollow never touches blocks/mutes and block never touches follows
// (three independent axes, per the social package's doc comment).
package pgrepo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/profile/social"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ social.Repo = (*Repo)(nil)

func (r *Repo) Follow(ctx context.Context, followerID, followeeID string) (*social.Relationship, error) {
	if followerID == followeeID {
		return nil, social.ErrSelfFollow
	}
	blocked, err := r.IsBlockedEitherWay(ctx, followerID, followeeID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, social.ErrBlockedByOp
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, followerID, followeeID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, followerID, followeeID)
}

func (r *Repo) Unfollow(ctx context.Context, followerID, followeeID string) (*social.Relationship, error) {
	if followerID == followeeID {
		return nil, social.ErrSelfFollow
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`, followerID, followeeID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, followerID, followeeID)
}

func (r *Repo) Block(ctx context.Context, blockerID, blockedID string) (*social.Relationship, error) {
	if blockerID == blockedID {
		return nil, social.ErrSelfBlock
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO blocks (blocker_id, blocked_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, blockerID, blockedID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, blockerID, blockedID)
}

func (r *Repo) Unblock(ctx context.Context, blockerID, blockedID string) (*social.Relationship, error) {
	_, err := r.pool.Exec(ctx, `DELETE FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`, blockerID, blockedID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, blockerID, blockedID)
}

func (r *Repo) Mute(ctx context.Context, muterID, mutedID string) (*social.Relationship, error) {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO mutes (muter_id, muted_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, muterID, mutedID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, muterID, mutedID)
}

func (r *Repo) Unmute(ctx context.Context, muterID, mutedID string) (*social.Relationship, error) {
	_, err := r.pool.Exec(ctx, `DELETE FROM mutes WHERE muter_id = $1 AND muted_id = $2`, muterID, mutedID)
	if err != nil {
		return nil, err
	}
	return r.GetRelationship(ctx, muterID, mutedID)
}

func (r *Repo) GetRelationship(ctx context.Context, viewerID, subjectID string) (*social.Relationship, error) {
	var rel social.Relationship
	err := r.pool.QueryRow(ctx, `
		SELECT
			EXISTS(SELECT 1 FROM follows WHERE follower_id=$1 AND followee_id=$2) AS following,
			EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$1) AS followed_by,
			EXISTS(SELECT 1 FROM blocks  WHERE blocker_id=$1  AND blocked_id=$2)  AS blocked,
			EXISTS(SELECT 1 FROM blocks  WHERE blocker_id=$2  AND blocked_id=$1)  AS blocked_by,
			EXISTS(SELECT 1 FROM mutes   WHERE muter_id=$1    AND muted_id=$2)    AS muted
	`, viewerID, subjectID).Scan(&rel.Following, &rel.FollowedBy, &rel.Blocked, &rel.BlockedBy, &rel.Muted)
	if err != nil {
		return nil, err
	}
	canContact := !rel.Blocked && !rel.BlockedBy
	rel.CanDM = canContact
	rel.CanCall = canContact
	return &rel, nil
}

func (r *Repo) GetFollowing(ctx context.Context, accountID string, _ bool, cursor string, limit int) ([]social.FollowEntry, string, error) {
	return r.listFollowEdge(ctx, "follower_id", "followee_id", accountID, cursor, limit)
}

func (r *Repo) GetFollowers(ctx context.Context, accountID string, cursor string, limit int) ([]social.FollowEntry, string, error) {
	return r.listFollowEdge(ctx, "followee_id", "follower_id", accountID, cursor, limit)
}

// listFollowEdge powers both GetFollowing and GetFollowers — they're the
// same query with the two follows columns swapped (who's fixed vs. who's
// listed), matching MemSocialRepo's near-identical twin implementations.
func (r *Repo) listFollowEdge(ctx context.Context, fixedCol, listedCol, accountID, cursor string, limit int) ([]social.FollowEntry, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT f.`+listedCol+`, p.display_name, p.avatar_url, f.created_at
		FROM follows f
		JOIN profiles p ON p.account_id = f.`+listedCol+`
		WHERE f.`+fixedCol+` = $1 AND ($2 = '' OR f.`+listedCol+` > $2)
		ORDER BY f.`+listedCol+`
		LIMIT $3`, accountID, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var entries []social.FollowEntry
	for rows.Next() {
		var e social.FollowEntry
		if err := rows.Scan(&e.AccountID, &e.DisplayName, &e.AvatarURL, &e.FollowedAt); err != nil {
			return nil, "", err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(entries) > limit {
		entries = entries[:limit]
		nextCursor = entries[limit-1].AccountID
	}
	return entries, nextCursor, nil
}

func (r *Repo) IsBlockedEitherWay(ctx context.Context, a, b string) (bool, error) {
	var blocked bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blocks
			WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1)
		)`, a, b).Scan(&blocked)
	return blocked, err
}
