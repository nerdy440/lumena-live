package chatsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumena/chat"
	"github.com/lumena/chat/chatsvc"
)

func noBlocks(context.Context, string, string) (bool, error) { return false, nil }
func allBlocked(context.Context, string, string) (bool, error) { return true, nil }
func allMutual(context.Context, string, string) (bool, error) { return true, nil }
func noneMutual(context.Context, string, string) (bool, error) { return false, nil }

func TestStartConversation_MutualGoesActive_NonMutualGoesRequest(t *testing.T) {
	ctx := context.Background()

	svcMutual := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual)
	c, err := svcMutual.StartConversation(ctx, "alice", "bob")
	if err != nil || c.State != chat.StateActive {
		t.Fatalf("expected active conversation for mutuals, got state=%v err=%v", c, err)
	}

	svcNonMutual := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, noneMutual)
	c2, err := svcNonMutual.StartConversation(ctx, "alice", "carol")
	if err != nil || c2.State != chat.StateRequest {
		t.Fatalf("expected request-state conversation for non-mutuals, got state=%v err=%v", c2, err)
	}
}

func TestStartConversation_BlockedRejected(t *testing.T) {
	svc := chatsvc.NewService(chat.NewMemChatRepo(), allBlocked, allMutual)
	_, err := svc.StartConversation(context.Background(), "alice", "bob")
	if !errors.Is(err, chat.ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
}

func TestStartConversation_SelfRejected(t *testing.T) {
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual)
	_, err := svc.StartConversation(context.Background(), "alice", "alice")
	if !errors.Is(err, chat.ErrSelfMessage) {
		t.Fatalf("expected ErrSelfMessage, got %v", err)
	}
}

func TestSendMessage_NonParticipantRejected(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual)
	c, _ := svc.StartConversation(ctx, "alice", "bob")

	_, _, err := svc.SendMessage(ctx, "mallory", c.ID, "m1", "hi", "")
	if !errors.Is(err, chat.ErrNotParticipant) {
		t.Fatalf("expected ErrNotParticipant, got %v", err)
	}
}

func TestSendMessage_BlockedMidConversationRejected(t *testing.T) {
	ctx := context.Background()
	repo := chat.NewMemChatRepo()
	blockedAfterStart := false
	isBlocked := func(context.Context, string, string) (bool, error) { return blockedAfterStart, nil }
	svc := chatsvc.NewService(repo, isBlocked, allMutual)

	c, err := svc.StartConversation(ctx, "alice", "bob")
	if err != nil {
		t.Fatal(err)
	}
	blockedAfterStart = true
	_, _, err = svc.SendMessage(ctx, "alice", c.ID, "m1", "hi", "")
	if !errors.Is(err, chat.ErrBlocked) {
		t.Fatalf("expected send after block to be rejected, got %v", err)
	}
}

func TestAcceptRequest_MovesToActiveAndInitiatorCannotAcceptOwnRequest(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, noneMutual)
	c, _ := svc.StartConversation(ctx, "alice", "bob") // alice initiates, non-mutual -> request

	if err := svc.AcceptRequest(ctx, "alice", c.ID); !errors.Is(err, chat.ErrCannotAcceptOwnRequest) {
		t.Fatalf("expected initiator cannot accept own request, got %v", err)
	}
	if err := svc.AcceptRequest(ctx, "bob", c.ID); err != nil {
		t.Fatalf("expected recipient to accept successfully, got %v", err)
	}

	requests, _, _ := svc.ListConversations(ctx, "bob", "requests", "", 10)
	if len(requests) != 0 {
		t.Fatalf("expected accepted conversation to leave the requests folder, got %+v", requests)
	}
	inbox, _, _ := svc.ListConversations(ctx, "bob", "inbox", "", 10)
	if len(inbox) != 1 {
		t.Fatalf("expected accepted conversation to appear in bob's inbox, got %+v", inbox)
	}
}

func TestSendMessage_AttachmentOnlyMessageAllowed(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual).
		WithAttachments(func(context.Context, string, string) (bool, error) { return true, nil })
	c, _ := svc.StartConversation(ctx, "alice", "bob")

	msg, _, err := svc.SendMessage(ctx, "alice", c.ID, "m1", "", "att-1")
	if err != nil {
		t.Fatalf("expected an attachment-only (empty body) message to be allowed, got %v", err)
	}
	if msg.AttachmentID != "att-1" {
		t.Fatalf("expected attachment id to be persisted, got %q", msg.AttachmentID)
	}
}

func TestSendMessage_EmptyBodyAndNoAttachmentRejected(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual)
	c, _ := svc.StartConversation(ctx, "alice", "bob")

	_, _, err := svc.SendMessage(ctx, "alice", c.ID, "m1", "", "")
	if !errors.Is(err, chat.ErrEmptyBody) {
		t.Fatalf("expected ErrEmptyBody, got %v", err)
	}
}

func TestSendMessage_UndeliverableAttachmentRejected(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual).
		WithAttachments(func(context.Context, string, string) (bool, error) { return false, nil }) // still scanning / blocked
	c, _ := svc.StartConversation(ctx, "alice", "bob")

	_, _, err := svc.SendMessage(ctx, "alice", c.ID, "m1", "check this out", "att-1")
	if !errors.Is(err, chat.ErrAttachmentNotDeliverable) {
		t.Fatalf("expected ErrAttachmentNotDeliverable, got %v", err)
	}
}

func TestSendMessage_AttachmentWithoutWithAttachmentsWiredIsRejected(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual) // WithAttachments never called
	c, _ := svc.StartConversation(ctx, "alice", "bob")

	_, _, err := svc.SendMessage(ctx, "alice", c.ID, "m1", "hi", "att-1")
	if !errors.Is(err, chat.ErrAttachmentNotDeliverable) {
		t.Fatalf("expected ErrAttachmentNotDeliverable when attachments aren't wired, got %v", err)
	}
}

func TestListMessages_DeletedForMeIsHiddenOnlyForThatViewer(t *testing.T) {
	ctx := context.Background()
	svc := chatsvc.NewService(chat.NewMemChatRepo(), noBlocks, allMutual)
	c, _ := svc.StartConversation(ctx, "alice", "bob")
	svc.SendMessage(ctx, "alice", c.ID, "m1", "hello", "")

	msg, _, _ := svc.ListMessages(ctx, "bob", c.ID, "", 10)
	if err := svc.DeleteMessage(ctx, "bob", msg[0].ID, "me"); err != nil {
		t.Fatal(err)
	}

	bobView, _, _ := svc.ListMessages(ctx, "bob", c.ID, "", 10)
	if len(bobView) != 0 {
		t.Fatalf("expected message hidden from bob after delete-for-me, got %+v", bobView)
	}
	aliceView, _, _ := svc.ListMessages(ctx, "alice", c.ID, "", 10)
	if len(aliceView) != 1 {
		t.Fatalf("expected alice to still see the message, got %+v", aliceView)
	}
}
