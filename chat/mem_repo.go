package chat

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemChatRepo is the in-memory dev/test implementation of Repo.
//
// List pagination (both ListConversations and ListMessages) cursors on a
// monotonic counter rather than a timestamp. profile/social's GetFollowing
// originally cursored on time.Time and broke when two writes landed in the
// same timestamp tick (identical wall-clock time under a fast test loop) —
// see that package's fix. Cursoring on a strictly-incrementing counter here
// avoids that class of bug entirely.
type MemChatRepo struct {
	mu            sync.RWMutex
	byID          map[string]*Conversation
	byPair        map[pairKey]string // (participantA,participantB) -> conversation id
	messages      map[string][]*Message // conversation id -> messages, append-only, seq order
	msgByClientID map[msgIdemKey]*Message
	msgByID       map[string]*Message
	globalOrder   uint64
	msgSeq        int
}

type pairKey struct{ a, b string }
type msgIdemKey struct{ conversationID, clientMsgID string }

func NewMemChatRepo() *MemChatRepo {
	return &MemChatRepo{
		byID:          make(map[string]*Conversation),
		byPair:        make(map[pairKey]string),
		messages:      make(map[string][]*Message),
		msgByClientID: make(map[msgIdemKey]*Message),
		msgByID:       make(map[string]*Message),
	}
}

var _ Repo = (*MemChatRepo)(nil)

func orderedPair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

func (r *MemChatRepo) GetOrCreateConversation(_ context.Context, accountA, accountB, initiatedBy string, initialState ConversationState) (*Conversation, error) {
	a, b := orderedPair(accountA, accountB)
	r.mu.Lock()
	defer r.mu.Unlock()

	if id, ok := r.byPair[pairKey{a, b}]; ok {
		c := r.byID[id]
		cp := *c
		return &cp, nil
	}

	r.globalOrder++
	c := &Conversation{
		ID:           "conv-" + a + "-" + b,
		ParticipantA: a,
		ParticipantB: b,
		InitiatedBy:  initiatedBy,
		State:        initialState,
		orderSeq:     r.globalOrder,
	}
	r.byID[c.ID] = c
	r.byPair[pairKey{a, b}] = c.ID
	cp := *c
	return &cp, nil
}

func (r *MemChatRepo) GetConversation(_ context.Context, id string) (*Conversation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	if !ok {
		return nil, ErrConversationNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *MemChatRepo) SetConversationState(_ context.Context, id string, state ConversationState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok {
		return ErrConversationNotFound
	}
	c.State = state
	return nil
}

// ListConversations returns folder="inbox" (active conversations, plus the
// viewer's own outgoing requests) or folder="requests" (pending requests
// where the viewer is the recipient, not the initiator).
func (r *MemChatRepo) ListConversations(_ context.Context, accountID string, folder string, cursor string, limit int) ([]ConversationSummary, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []*Conversation
	for _, c := range r.byID {
		if !c.HasParticipant(accountID) {
			continue
		}
		switch folder {
		case "requests":
			if c.State == StateRequest && c.InitiatedBy != accountID {
				matches = append(matches, c)
			}
		default: // "inbox"
			if c.State == StateActive || (c.State == StateRequest && c.InitiatedBy == accountID) {
				matches = append(matches, c)
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].orderSeq > matches[j].orderSeq })

	start := 0
	if cursor != "" {
		for i, c := range matches {
			if c.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	matches = matches[start:]

	var out []ConversationSummary
	for _, c := range matches {
		if len(out) >= limit+1 {
			break
		}
		out = append(out, ConversationSummary{
			Conversation: *c,
			LastMessage:  r.lastMessageLocked(c.ID),
			UnreadCount:  r.unreadCountLocked(c.ID, accountID),
		})
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = out[limit-1].Conversation.ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func (r *MemChatRepo) lastMessageLocked(conversationID string) *Message {
	msgs := r.messages[conversationID]
	if len(msgs) == 0 {
		return nil
	}
	m := *msgs[len(msgs)-1]
	return &m
}

func (r *MemChatRepo) unreadCountLocked(conversationID, viewerID string) int {
	n := 0
	for _, m := range r.messages[conversationID] {
		if m.SenderID != viewerID && m.ReadAt == nil && !m.DeletedForMe[viewerID] && !m.DeletedForAll {
			n++
		}
	}
	return n
}

func (r *MemChatRepo) InsertMessage(_ context.Context, conversationID, senderID, clientMsgID, body, attachmentID string) (*Message, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.msgByClientID[msgIdemKey{conversationID, clientMsgID}]; ok {
		cp := *existing
		return &cp, false, nil
	}
	c, ok := r.byID[conversationID]
	if !ok {
		return nil, false, ErrConversationNotFound
	}

	r.msgSeq++
	r.globalOrder++
	m := &Message{
		ID:             "msg-" + itoa(r.msgSeq),
		ConversationID: conversationID,
		SenderID:       senderID,
		ClientMsgID:    clientMsgID,
		Body:           body,
		AttachmentID:   attachmentID,
		Seq:            uint64(len(r.messages[conversationID]) + 1),
		DeliveredAt:    time.Now(),
		DeletedForMe:   make(map[string]bool),
		CreatedAt:      time.Now(),
	}
	r.messages[conversationID] = append(r.messages[conversationID], m)
	r.msgByClientID[msgIdemKey{conversationID, clientMsgID}] = m
	r.msgByID[m.ID] = m
	c.LastMessageAt = m.CreatedAt
	c.orderSeq = r.globalOrder

	cp := *m
	return &cp, true, nil
}

func (r *MemChatRepo) ListMessages(_ context.Context, conversationID, cursor string, limit int) ([]Message, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := r.messages[conversationID]
	start := 0
	if cursor != "" {
		if n, ok := parseUint(cursor); ok {
			start = len(all)
			for i, m := range all {
				if m.Seq > n {
					start = i
					break
				}
			}
		}
	}
	rest := all[start:]

	var out []Message
	for _, m := range rest {
		if len(out) >= limit+1 {
			break
		}
		out = append(out, *m)
	}
	var nextCursor string
	if len(out) > limit {
		nextCursor = itoa(int(out[limit-1].Seq))
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func (r *MemChatRepo) GetMessage(_ context.Context, messageID string) (*Message, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.msgByID[messageID]
	if !ok {
		return nil, ErrMessageNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *MemChatRepo) MarkRead(_ context.Context, conversationID, viewerID string, upToSeq uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[conversationID]; !ok {
		return ErrConversationNotFound
	}
	now := time.Now()
	for _, m := range r.messages[conversationID] {
		if m.SenderID != viewerID && m.Seq <= upToSeq && m.ReadAt == nil {
			m.ReadAt = &now
		}
	}
	return nil
}

func (r *MemChatRepo) DeleteMessage(_ context.Context, messageID, viewerID string, everyone bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.msgByID[messageID]
	if !ok {
		return ErrMessageNotFound
	}
	c := r.byID[m.ConversationID]
	if c == nil || !c.HasParticipant(viewerID) {
		return ErrNotParticipant
	}
	if everyone {
		if m.SenderID != viewerID {
			return ErrNotParticipant
		}
		m.DeletedForAll = true
		m.Body = ""
	} else {
		m.DeletedForMe[viewerID] = true
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}
