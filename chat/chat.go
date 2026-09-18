// Package chat implements private 1-to-1 messaging (doc 06 §6, doc 07 §7,
// roadmap Phase 7). Room chat (COMMENT on room:{id}) lives in the gateway
// module instead — this package is direct messages only.
package chat

import (
	"context"
	"errors"
	"time"
)

// ConversationState mirrors the `state` column in doc 06's conversations
// table: active|request|blocked, plus `declined` for a request the
// recipient has explicitly turned down.
type ConversationState string

const (
	StateActive   ConversationState = "active"
	StateRequest  ConversationState = "request"
	StateDeclined ConversationState = "declined"
)

var (
	ErrSelfMessage          = errors.New("chat: cannot message yourself")
	ErrBlocked               = errors.New("chat: blocked — cannot send or receive messages")
	ErrConversationNotFound  = errors.New("chat: conversation not found")
	ErrNotParticipant        = errors.New("chat: not a participant in this conversation")
	ErrEmptyBody             = errors.New("chat: message body is empty")
	ErrMessageNotFound       = errors.New("chat: message not found")
	ErrCannotAcceptOwnRequest = errors.New("chat: cannot accept/decline your own outgoing request")
	ErrAttachmentNotDeliverable = errors.New("chat: attachment is not deliverable (unscanned, blocked, or not owned by sender)")
)

// Conversation is one 1-to-1 thread. ParticipantA < ParticipantB
// lexicographically (doc 06's `ordered_pair` constraint) so there is
// exactly one conversation row per pair regardless of who messaged first.
type Conversation struct {
	ID            string            `json:"id"`
	ParticipantA  string            `json:"participant_a"`
	ParticipantB  string            `json:"participant_b"`
	InitiatedBy   string            `json:"initiated_by"`
	State         ConversationState `json:"state"`
	LastMessageAt time.Time         `json:"last_message_at"`
	// orderSeq is an internal monotonic counter used for list pagination —
	// see mem_repo.go's doc comment on why LastMessageAt alone isn't safe
	// as a cursor key. Lowercase (unexported) already keeps it out of JSON;
	// no tag needed.
	orderSeq uint64
}

// Other returns the participant that is not accountID.
func (c *Conversation) Other(accountID string) string {
	if c.ParticipantA == accountID {
		return c.ParticipantB
	}
	return c.ParticipantA
}

// HasParticipant reports whether accountID is a party to this conversation.
func (c *Conversation) HasParticipant(accountID string) bool {
	return c.ParticipantA == accountID || c.ParticipantB == accountID
}

// Message is one direct message (doc 06's direct_messages table).
type Message struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	SenderID       string     `json:"sender_id"`
	ClientMsgID    string     `json:"client_msg_id"` // client-assigned dedupe key (UNIQUE with conversation_id in doc 06)
	Body           string     `json:"body"`
	AttachmentID   string     `json:"attachment_id,omitempty"` // references an attachsvc.Attachment that was already scanned clean (doc 06 §6, doc 07 §8)
	Seq            uint64     `json:"seq"`
	DeliveredAt    time.Time  `json:"delivered_at"`
	ReadAt         *time.Time `json:"read_at,omitempty"` // set when the recipient (not the sender) has read up to this message
	// DeletedForMe is per-viewer and never serialized — whether account X
	// deleted a message "for me" is X's own business, not something the
	// other participant (or any API consumer) should ever be able to read
	// off this struct. json:"-" the same way match.Candidate excludes
	// score_reasons.
	DeletedForMe  map[string]bool `json:"-"`
	DeletedForAll bool            `json:"deleted_for_all,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ConversationSummary is what ListConversations returns: enough to render
// an inbox row without a second round trip per conversation.
type ConversationSummary struct {
	Conversation Conversation `json:"conversation"`
	LastMessage  *Message     `json:"last_message,omitempty"`
	UnreadCount  int          `json:"unread_count"`
}

// Repo is the chat data-access contract.
type Repo interface {
	// GetOrCreateConversation returns the existing conversation for the pair,
	// or creates one with the given initial state if none exists yet.
	GetOrCreateConversation(ctx context.Context, accountA, accountB, initiatedBy string, initialState ConversationState) (*Conversation, error)
	GetConversation(ctx context.Context, id string) (*Conversation, error)
	SetConversationState(ctx context.Context, id string, state ConversationState) error
	ListConversations(ctx context.Context, accountID string, folder string, cursor string, limit int) ([]ConversationSummary, string, error)

	// InsertMessage is idempotent on (conversationID, clientMsgID): a retry
	// with the same clientMsgID returns the original message and isNew=false
	// instead of creating a duplicate (doc 06 §6's fix for flaky-network
	// double-sends).
	InsertMessage(ctx context.Context, conversationID, senderID, clientMsgID, body, attachmentID string) (msg *Message, isNew bool, err error)
	ListMessages(ctx context.Context, conversationID, cursor string, limit int) ([]Message, string, error)
	GetMessage(ctx context.Context, messageID string) (*Message, error)
	MarkRead(ctx context.Context, conversationID, viewerID string, upToSeq uint64) error
	DeleteMessage(ctx context.Context, messageID, viewerID string, everyone bool) error
}
