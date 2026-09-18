// Package pgrepo implements feed.RoomRepo, feed.PremiumUnlockRepo, and
// feed.NotificationRepo against real Postgres — the production
// counterpart to feed's Mem* repos.
package pgrepo

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/feed"
)

// ─── Rooms ──────────────────────────────────────────────────────────────────

type RoomRepo struct {
	pool *pgxpool.Pool
}

func NewRoomRepo(pool *pgxpool.Pool) *RoomRepo {
	return &RoomRepo{pool: pool}
}

var _ feed.RoomRepo = (*RoomRepo)(nil)

const roomCols = `room_id, host_id, host_name, host_avatar, host_level, title, cover_url, tags,
	language, region_code, viewer_count, peak_viewers, status, started_at, ended_at,
	gift_velocity, follower_growth_rate, is_premium, unlock_price_coins`

func scanRoom(row pgx.Row) (*feed.Room, error) {
	var r feed.Room
	var status string
	err := row.Scan(&r.RoomID, &r.HostID, &r.HostName, &r.HostAvatar, &r.HostLevel, &r.Title, &r.CoverURL, &r.Tags,
		&r.Language, &r.RegionCode, &r.ViewerCount, &r.PeakViewers, &status, &r.StartedAt, &r.EndedAt,
		&r.GiftVelocity, &r.FollowerGrowthRate, &r.IsPremium, &r.UnlockPriceCoins)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, feed.ErrRoomNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = feed.RoomStatus(status)
	return &r, nil
}

func (r *RoomRepo) ListLive(ctx context.Context) ([]feed.Room, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+roomCols+` FROM feed_rooms WHERE status = 'live'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []feed.Room
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *room)
	}
	return out, rows.Err()
}

func (r *RoomRepo) Get(ctx context.Context, roomID string) (*feed.Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `SELECT `+roomCols+` FROM feed_rooms WHERE room_id = $1`, roomID))
}

func (r *RoomRepo) GetByHost(ctx context.Context, hostID string) (*feed.Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `SELECT `+roomCols+` FROM feed_rooms WHERE host_id = $1 AND status = 'live' LIMIT 1`, hostID))
}

func (r *RoomRepo) ListByHosts(ctx context.Context, hostIDs []string) ([]feed.Room, error) {
	if len(hostIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+roomCols+` FROM feed_rooms WHERE status = 'live' AND host_id = ANY($1)`, hostIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []feed.Room
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *room)
	}
	return out, rows.Err()
}

func (r *RoomRepo) Create(ctx context.Context, room feed.Room) (*feed.Room, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('feed_room_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	room.RoomID = "room-" + strconv.FormatInt(seq, 10)
	now := time.Now()
	room.StartedAt = &now
	room.Status = feed.RoomLive
	if room.Tags == nil {
		room.Tags = []string{}
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO feed_rooms (room_id, host_id, host_name, host_avatar, host_level, title, cover_url, tags,
			language, region_code, viewer_count, peak_viewers, status, started_at, gift_velocity, follower_growth_rate,
			is_premium, unlock_price_coins)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING `+roomCols,
		room.RoomID, room.HostID, room.HostName, room.HostAvatar, room.HostLevel, room.Title, room.CoverURL, room.Tags,
		room.Language, room.RegionCode, room.ViewerCount, room.PeakViewers, string(room.Status), room.StartedAt,
		room.GiftVelocity, room.FollowerGrowthRate, room.IsPremium, room.UnlockPriceCoins)
	return scanRoom(row)
}

func (r *RoomRepo) UpdateViewerCount(ctx context.Context, roomID string, count int) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE feed_rooms SET viewer_count = $2, peak_viewers = GREATEST(peak_viewers, $2) WHERE room_id = $1`,
		roomID, count)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return feed.ErrRoomNotFound
	}
	return nil
}

func (r *RoomRepo) End(ctx context.Context, roomID string, _ string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE feed_rooms SET status = 'ended', ended_at = now() WHERE room_id = $1`, roomID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return feed.ErrRoomNotFound
	}
	return nil
}

func (r *RoomRepo) SetPremium(ctx context.Context, roomID string, isPremium bool, unlockPriceCoins int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE feed_rooms SET is_premium = $2, unlock_price_coins = $3 WHERE room_id = $1`,
		roomID, isPremium, unlockPriceCoins)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return feed.ErrRoomNotFound
	}
	return nil
}

// ─── Premium unlocks ────────────────────────────────────────────────────────

type PremiumUnlockRepo struct {
	pool *pgxpool.Pool
}

func NewPremiumUnlockRepo(pool *pgxpool.Pool) *PremiumUnlockRepo {
	return &PremiumUnlockRepo{pool: pool}
}

var _ feed.PremiumUnlockRepo = (*PremiumUnlockRepo)(nil)

func (r *PremiumUnlockRepo) HasUnlocked(ctx context.Context, roomID, accountID string) (bool, error) {
	var unlocked bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM feed_premium_unlocks WHERE room_id = $1 AND account_id = $2)`,
		roomID, accountID).Scan(&unlocked)
	return unlocked, err
}

func (r *PremiumUnlockRepo) RecordUnlock(ctx context.Context, roomID, accountID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO feed_premium_unlocks (room_id, account_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		roomID, accountID)
	return err
}

// ─── Notifications ──────────────────────────────────────────────────────────

type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

var _ feed.NotificationRepo = (*NotificationRepo)(nil)

func (r *NotificationRepo) List(ctx context.Context, accountID, cursor string, limit int) ([]feed.Notification, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var afterSeq int64
	if cursor != "" {
		if v, err := strconv.ParseInt(cursor, 10, 64); err == nil {
			afterSeq = v
		}
	}
	// order_seq descending (newest first), so "after cursor" means "seq
	// less than the last one already seen" — matching feed_conversations'
	// same descending-cursor convention.
	var rows pgx.Rows
	var err error
	if afterSeq > 0 {
		rows, err = r.pool.Query(ctx, `
			SELECT id, type, actor_id, actor_name, body, read, created_at, deep_link, order_seq
			FROM feed_notifications WHERE account_id = $1 AND order_seq < $2
			ORDER BY order_seq DESC LIMIT $3`, accountID, afterSeq, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, type, actor_id, actor_name, body, read, created_at, deep_link, order_seq
			FROM feed_notifications WHERE account_id = $1
			ORDER BY order_seq DESC LIMIT $2`, accountID, limit+1)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	type row struct {
		n   feed.Notification
		seq int64
	}
	var items []row
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.n.ID, &rr.n.Type, &rr.n.ActorID, &rr.n.ActorName, &rr.n.Body, &rr.n.Read, &rr.n.CreatedAt, &rr.n.DeepLink, &rr.seq); err != nil {
			return nil, "", err
		}
		items = append(items, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(items) > limit {
		items = items[:limit]
		nextCursor = strconv.FormatInt(items[limit-1].seq, 10)
	}
	out := make([]feed.Notification, len(items))
	for i, it := range items {
		out[i] = it.n
	}
	return out, nextCursor, nil
}

func (r *NotificationRepo) MarkRead(ctx context.Context, accountID, upToID string) error {
	var upToSeq int64
	if err := r.pool.QueryRow(ctx, `SELECT order_seq FROM feed_notifications WHERE id = $1`, upToID).Scan(&upToSeq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE feed_notifications SET read = true WHERE account_id = $1 AND order_seq >= $2`,
		accountID, upToSeq)
	return err
}

func (r *NotificationRepo) GetSummary(ctx context.Context, accountID string) (*feed.NotificationSummary, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM feed_notifications WHERE account_id = $1 AND read = false`, accountID).Scan(&count)
	if err != nil {
		return nil, err
	}
	return &feed.NotificationSummary{UnreadCount: count}, nil
}

// Create buckets the notification under its own ActorID — faithfully
// mirroring feed.MemNotificationRepo.Create's exact (slightly odd)
// behavior. See this package's migration comment: Create has no caller
// anywhere in this codebase today.
func (r *NotificationRepo) Create(ctx context.Context, n feed.Notification) error {
	if n.ActorID == nil {
		return nil
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('feed_notification_seq')`).Scan(&seq); err != nil {
		return err
	}
	if n.ID == "" {
		n.ID = "notif-" + strconv.FormatInt(seq, 10)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO feed_notifications (id, account_id, type, actor_id, actor_name, body, read, created_at, deep_link, order_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		n.ID, *n.ActorID, n.Type, n.ActorID, n.ActorName, n.Body, n.Read, n.CreatedAt, n.DeepLink, seq)
	return err
}
