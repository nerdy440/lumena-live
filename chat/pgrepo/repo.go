// Package pgrepo implements chat.Repo against real Postgres — the
// production counterpart to chat.MemChatRepo, matching its semantics:
// exactly one conversation row per ordered participant pair,
// idempotent-on-(conversation,client_msg_id) message inserts, and a
// single monotonic order_seq (a Postgres sequence, standing in for
// MemChatRepo's globalOrder counter) so ListConversations' "most
// recently active first" ordering matches exactly.
package pgrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/chat"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ chat.Repo = (*Repo)(nil)

func orderedPair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

const convCols = "id, participant_a, participant_b, initiated_by, state, last_message_at"

func scanConversation(row pgx.Row) (*chat.Conversation, error) {
	var c chat.Conversation
	var lastMessageAt *time.Time
	err := row.Scan(&c.ID, &c.ParticipantA, &c.ParticipantB, &c.InitiatedBy, &c.State, &lastMessageAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, chat.ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastMessageAt != nil {
		c.LastMessageAt = *lastMessageAt
	}
	return &c, nil
}

func (r *Repo) GetOrCreateConversation(ctx context.Context, accountA, accountB, initiatedBy string, initialState chat.ConversationState) (*chat.Conversation, error) {
	a, b := orderedPair(accountA, accountB)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO conversations (id, participant_a, participant_b, initiated_by, state)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (participant_a, participant_b) DO UPDATE SET participant_a = EXCLUDED.participant_a
		RETURNING `+convCols,
		"conv-"+a+"-"+b, a, b, initiatedBy, initialState)
	return scanConversation(row)
}

func (r *Repo) GetConversation(ctx context.Context, id string) (*chat.Conversation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1`, id)
	return scanConversation(row)
}

func (r *Repo) SetConversationState(ctx context.Context, id string, state chat.ConversationState) error {
	tag, err := r.pool.Exec(ctx, `UPDATE conversations SET state = $2 WHERE id = $1`, id, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrConversationNotFound
	}
	return nil
}

func (r *Repo) ListConversations(ctx context.Context, accountID string, folder string, cursor string, limit int) ([]chat.ConversationSummary, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var cursorSeq int64
	if cursor != "" {
		v, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", nil
		}
		cursorSeq = v
	}

	var whereFolder string
	if folder == "requests" {
		whereFolder = "c.state = 'request' AND c.initiated_by <> $1"
	} else {
		whereFolder = "(c.state = 'active' OR (c.state = 'request' AND c.initiated_by = $1))"
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+convCols+`, c.order_seq
		FROM conversations c
		WHERE (c.participant_a = $1 OR c.participant_b = $1)
			AND `+whereFolder+`
			AND ($2 = 0 OR c.order_seq < $2)
		ORDER BY c.order_seq DESC
		LIMIT $3`, accountID, cursorSeq, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	type row struct {
		c        chat.Conversation
		orderSeq int64
	}
	var matched []row
	for rows.Next() {
		var rr row
		var lastMessageAt *time.Time
		if err := rows.Scan(&rr.c.ID, &rr.c.ParticipantA, &rr.c.ParticipantB, &rr.c.InitiatedBy, &rr.c.State, &lastMessageAt, &rr.orderSeq); err != nil {
			return nil, "", err
		}
		if lastMessageAt != nil {
			rr.c.LastMessageAt = *lastMessageAt
		}
		matched = append(matched, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var out []chat.ConversationSummary
	for _, rr := range matched {
		lastMsg, err := r.lastMessage(ctx, rr.c.ID)
		if err != nil {
			return nil, "", err
		}
		unread, err := r.unreadCount(ctx, rr.c.ID, accountID)
		if err != nil {
			return nil, "", err
		}
		out = append(out, chat.ConversationSummary{Conversation: rr.c, LastMessage: lastMsg, UnreadCount: unread})
	}

	var nextCursor string
	if len(out) > limit {
		out = out[:limit]
		nextCursor = strconv.FormatInt(matched[limit-1].orderSeq, 10)
	}
	return out, nextCursor, nil
}

const msgCols = "id, conversation_id, sender_id, client_msg_id, body, attachment_id, seq, delivered_at, read_at, deleted_for_all, created_at"

func scanMessage(row pgx.Row) (*chat.Message, error) {
	var m chat.Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.ClientMsgID, &m.Body, &m.AttachmentID,
		&m.Seq, &m.DeliveredAt, &m.ReadAt, &m.DeletedForAll, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, chat.ErrMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Repo) lastMessage(ctx context.Context, conversationID string) (*chat.Message, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+msgCols+` FROM direct_messages WHERE conversation_id = $1 ORDER BY seq DESC LIMIT 1`, conversationID)
	m, err := scanMessage(row)
	if errors.Is(err, chat.ErrMessageNotFound) {
		return nil, nil
	}
	return m, err
}

func (r *Repo) unreadCount(ctx context.Context, conversationID, viewerID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM direct_messages m
		WHERE m.conversation_id = $1 AND m.sender_id <> $2 AND m.read_at IS NULL AND m.deleted_for_all = false
			AND NOT EXISTS (SELECT 1 FROM message_deletes d WHERE d.message_id = m.id AND d.account_id = $2)
	`, conversationID, viewerID).Scan(&n)
	return n, err
}

// InsertMessage assigns each message the next sequence number for its
// conversation and bumps the conversation's order_seq/last_message_at in
// the same transaction — see this package's doc comment for why order_seq
// exists at all.
func (r *Repo) InsertMessage(ctx context.Context, conversationID, senderID, clientMsgID, body, attachmentID string) (*chat.Message, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1)`, conversationID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, chat.ErrConversationNotFound
	}

	if existing, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+msgCols+` FROM direct_messages WHERE conversation_id = $1 AND client_msg_id = $2`,
		conversationID, clientMsgID)); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, chat.ErrMessageNotFound) {
		return nil, false, err
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO direct_messages (id, conversation_id, sender_id, client_msg_id, body, attachment_id, seq)
		SELECT $1, $2, $3, $4, $5, $6, COALESCE(MAX(seq), 0) + 1 FROM direct_messages WHERE conversation_id = $2
		RETURNING `+msgCols,
		newID("msg"), conversationID, senderID, clientMsgID, body, attachmentID)
	m, err := scanMessage(row)
	if err != nil {
		return nil, false, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE conversations SET last_message_at = $2, order_seq = nextval('chat_order_seq') WHERE id = $1`,
		conversationID, m.CreatedAt); err != nil {
		return nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return m, true, nil
}

func (r *Repo) ListMessages(ctx context.Context, conversationID, cursor string, limit int) ([]chat.Message, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var afterSeq uint64
	if cursor != "" {
		v, err := strconv.ParseUint(cursor, 10, 64)
		if err == nil {
			afterSeq = v
		}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+msgCols+` FROM direct_messages
		WHERE conversation_id = $1 AND seq > $2
		ORDER BY seq ASC
		LIMIT $3`, conversationID, afterSeq, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []chat.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(out) > limit {
		out = out[:limit]
		nextCursor = strconv.FormatUint(out[limit-1].Seq, 10)
	}
	return out, nextCursor, nil
}

func (r *Repo) GetMessage(ctx context.Context, messageID string) (*chat.Message, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+msgCols+` FROM direct_messages WHERE id = $1`, messageID)
	return scanMessage(row)
}

func (r *Repo) MarkRead(ctx context.Context, conversationID, viewerID string, upToSeq uint64) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1)`, conversationID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return chat.ErrConversationNotFound
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE direct_messages SET read_at = now()
		WHERE conversation_id = $1 AND sender_id <> $2 AND seq <= $3 AND read_at IS NULL`,
		conversationID, viewerID, upToSeq)
	return err
}

func (r *Repo) DeleteMessage(ctx context.Context, messageID, viewerID string, everyone bool) error {
	var senderID, conversationID string
	err := r.pool.QueryRow(ctx, `SELECT sender_id, conversation_id FROM direct_messages WHERE id = $1`, messageID).Scan(&senderID, &conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return chat.ErrMessageNotFound
	}
	if err != nil {
		return err
	}

	var isParticipant bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1 AND (participant_a = $2 OR participant_b = $2))`,
		conversationID, viewerID).Scan(&isParticipant); err != nil {
		return err
	}
	if !isParticipant {
		return chat.ErrNotParticipant
	}

	if everyone {
		if senderID != viewerID {
			return chat.ErrNotParticipant
		}
		_, err := r.pool.Exec(ctx, `UPDATE direct_messages SET deleted_for_all = true, body = '' WHERE id = $1`, messageID)
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO message_deletes (message_id, account_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		messageID, viewerID)
	return err
}
