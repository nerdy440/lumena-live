// Package chatsvc is the business-logic layer over chat.Repo: block
// enforcement, the mutual-follow request-folder rule, and participant
// authorization. It depends on profile/social's block and follow state only
// through injected function types, so this module never imports profile —
// matching the DI pattern gateway/ws uses for its host check.
package chatsvc

import (
	"context"

	"github.com/lumena/chat"
)

// IsBlockedFunc reports whether a and b are blocked in either direction.
type IsBlockedFunc func(ctx context.Context, a, b string) (bool, error)

// IsMutualFollowFunc reports whether a and b follow each other. A new
// conversation between mutuals starts "active" (inbox); otherwise it starts
// "request" (roadmap Phase 7: "Request folder (non-mutual messages)").
type IsMutualFollowFunc func(ctx context.Context, a, b string) (bool, error)

// IsAttachmentDeliverableFunc reports whether attachmentID has passed
// scanning and is owned by accountID (doc 07 §8, roadmap Phase 10). Injected
// so this module doesn't need to import attachsvc directly.
type IsAttachmentDeliverableFunc func(ctx context.Context, attachmentID, accountID string) (bool, error)

// MessageSentHook is notified after a message is successfully persisted, so
// the moderation module's grooming-risk engine (Phase 15, doc 10 §2d) can
// observe DM traffic without chatsvc importing moderation. Optional.
type MessageSentHook func(ctx context.Context, senderID, recipientID, body string)

type Service struct {
	repo           chat.Repo
	isBlocked      IsBlockedFunc
	isMutual       IsMutualFollowFunc
	isDeliverable  IsAttachmentDeliverableFunc
	onMessageSent  MessageSentHook
}

func NewService(repo chat.Repo, isBlocked IsBlockedFunc, isMutual IsMutualFollowFunc) *Service {
	return &Service{repo: repo, isBlocked: isBlocked, isMutual: isMutual}
}

// WithAttachments enables attachment_id validation on SendMessage (doc 07
// §8). Without it, any non-empty attachment_id is rejected — Phase 7's
// chat can run without Phase 10's attachment service present.
func (s *Service) WithAttachments(isDeliverable IsAttachmentDeliverableFunc) *Service {
	s.isDeliverable = isDeliverable
	return s
}

// WithMessageHook registers the moderation module's risk-signal observer.
func (s *Service) WithMessageHook(hook MessageSentHook) *Service {
	s.onMessageSent = hook
	return s
}

// StartConversation returns the (possibly pre-existing) conversation
// between accountID and otherID, refusing if either has blocked the other.
func (s *Service) StartConversation(ctx context.Context, accountID, otherID string) (*chat.Conversation, error) {
	if accountID == otherID {
		return nil, chat.ErrSelfMessage
	}
	blocked, err := s.isBlocked(ctx, accountID, otherID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, chat.ErrBlocked
	}

	mutual, err := s.isMutual(ctx, accountID, otherID)
	if err != nil {
		return nil, err
	}
	initialState := chat.StateRequest
	if mutual {
		initialState = chat.StateActive
	}
	return s.repo.GetOrCreateConversation(ctx, accountID, otherID, accountID, initialState)
}

// SendMessage sends body in conversationID as senderID. Idempotent on
// clientMsgID (doc 06 §6 / roadmap exit gate: "message deduplication on
// retry, same client_msg_id, confirm single delivery").
func (s *Service) SendMessage(ctx context.Context, senderID, conversationID, clientMsgID, body, attachmentID string) (msg *chat.Message, isNew bool, err error) {
	if body == "" && attachmentID == "" {
		return nil, false, chat.ErrEmptyBody
	}
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, false, err
	}
	if !conv.HasParticipant(senderID) {
		return nil, false, chat.ErrNotParticipant
	}
	other := conv.Other(senderID)
	blocked, err := s.isBlocked(ctx, senderID, other)
	if err != nil {
		return nil, false, err
	}
	if blocked {
		return nil, false, chat.ErrBlocked
	}
	if attachmentID != "" {
		if s.isDeliverable == nil {
			return nil, false, chat.ErrAttachmentNotDeliverable
		}
		ok, err := s.isDeliverable(ctx, attachmentID, senderID)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, chat.ErrAttachmentNotDeliverable
		}
	}
	msg, isNew, err = s.repo.InsertMessage(ctx, conversationID, senderID, clientMsgID, body, attachmentID)
	if err == nil && isNew && s.onMessageSent != nil {
		s.onMessageSent(ctx, senderID, other, body)
	}
	return msg, isNew, err
}

// IsParticipant reports whether accountID is a party to conversationID.
// Exposed so the realtime gateway (gateway/ws.IsParticipantFunc) can
// authorize conv:{id} subscriptions without this module depending on the
// gateway module.
func (s *Service) IsParticipant(ctx context.Context, conversationID, accountID string) (bool, error) {
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return false, err
	}
	return conv.HasParticipant(accountID), nil
}

func (s *Service) ListConversations(ctx context.Context, accountID, folder, cursor string, limit int) ([]chat.ConversationSummary, string, error) {
	return s.repo.ListConversations(ctx, accountID, folder, cursor, limit)
}

func (s *Service) ListMessages(ctx context.Context, viewerID, conversationID, cursor string, limit int) ([]chat.Message, string, error) {
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, "", err
	}
	if !conv.HasParticipant(viewerID) {
		return nil, "", chat.ErrNotParticipant
	}
	all, next, err := s.repo.ListMessages(ctx, conversationID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	visible := all[:0]
	for _, m := range all {
		if m.DeletedForMe[viewerID] {
			continue
		}
		visible = append(visible, m)
	}
	return visible, next, nil
}

func (s *Service) MarkRead(ctx context.Context, viewerID, conversationID string, upToSeq uint64) error {
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if !conv.HasParticipant(viewerID) {
		return chat.ErrNotParticipant
	}
	return s.repo.MarkRead(ctx, conversationID, viewerID, upToSeq)
}

func (s *Service) DeleteMessage(ctx context.Context, viewerID, messageID, scope string) error {
	return s.repo.DeleteMessage(ctx, messageID, viewerID, scope == "everyone")
}

// AcceptRequest moves a pending, incoming request into the active inbox.
func (s *Service) AcceptRequest(ctx context.Context, viewerID, conversationID string) error {
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if !conv.HasParticipant(viewerID) {
		return chat.ErrNotParticipant
	}
	if conv.InitiatedBy == viewerID {
		return chat.ErrCannotAcceptOwnRequest
	}
	return s.repo.SetConversationState(ctx, conversationID, chat.StateActive)
}

// DeclineRequest marks a pending, incoming request declined so it drops out
// of both the inbox and the requests folder.
func (s *Service) DeclineRequest(ctx context.Context, viewerID, conversationID string) error {
	conv, err := s.repo.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if !conv.HasParticipant(viewerID) {
		return chat.ErrNotParticipant
	}
	if conv.InitiatedBy == viewerID {
		return chat.ErrCannotAcceptOwnRequest
	}
	return s.repo.SetConversationState(ctx, conversationID, chat.StateDeclined)
}
