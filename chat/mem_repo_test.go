package chat_test

import (
	"context"
	"testing"

	"github.com/lumena/chat"
)

func TestGetOrCreateConversation_OneRowPerPairRegardlessOfOrder(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()

	c1, err := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := r.GetOrCreateConversation(ctx, "bob", "alice", "bob", chat.StateRequest)
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("expected same conversation id regardless of pair order, got %q vs %q", c1.ID, c2.ID)
	}
	if c2.State != chat.StateActive {
		t.Fatalf("expected the second call to return the existing conversation's state (active), got %q", c2.State)
	}
}

func TestInsertMessage_IdempotentOnClientMsgID(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	c, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)

	m1, isNew1, err := r.InsertMessage(ctx, c.ID, "alice", "client-1", "hello", "")
	if err != nil || !isNew1 {
		t.Fatalf("expected new message, got isNew=%v err=%v", isNew1, err)
	}
	m2, isNew2, err := r.InsertMessage(ctx, c.ID, "alice", "client-1", "hello (retry)", "")
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 {
		t.Fatal("expected retry with same client_msg_id to not create a new message")
	}
	if m1.ID != m2.ID || m1.Body != m2.Body {
		t.Fatalf("expected retry to return the original message, got %+v vs %+v", m1, m2)
	}

	msgs, _, _ := r.ListMessages(ctx, c.ID, "", 10)
	if len(msgs) != 1 {
		t.Fatalf("expected exactly one persisted message despite the retry, got %d", len(msgs))
	}
}

func TestListMessages_CursorPagination(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	c, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	for i := 0; i < 5; i++ {
		r.InsertMessage(ctx, c.ID, "alice", "m"+string(rune('0'+i)), "msg", "")
	}

	page1, cursor, err := r.ListMessages(ctx, c.ID, "", 2)
	if err != nil || len(page1) != 2 || cursor == "" {
		t.Fatalf("page1: got %d messages, cursor=%q, err=%v", len(page1), cursor, err)
	}
	page2, cursor2, err := r.ListMessages(ctx, c.ID, cursor, 2)
	if err != nil || len(page2) != 2 || cursor2 == "" {
		t.Fatalf("page2: got %d messages, cursor=%q, err=%v", len(page2), cursor2, err)
	}
	page3, cursor3, err := r.ListMessages(ctx, c.ID, cursor2, 2)
	if err != nil || len(page3) != 1 || cursor3 != "" {
		t.Fatalf("page3: got %d messages, cursor=%q, err=%v", len(page3), cursor3, err)
	}
	seen := map[string]bool{}
	for _, m := range append(append(page1, page2...), page3...) {
		if seen[m.ID] {
			t.Errorf("duplicate message %s across pages", m.ID)
		}
		seen[m.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 unique messages across pages, got %d", len(seen))
	}
}

func TestMarkRead_OnlyAffectsMessagesFromOtherParticipant(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	c, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	r.InsertMessage(ctx, c.ID, "alice", "a1", "from alice", "")
	r.InsertMessage(ctx, c.ID, "bob", "b1", "from bob", "")

	if err := r.MarkRead(ctx, c.ID, "alice", 2); err != nil {
		t.Fatal(err)
	}
	msgs, _, _ := r.ListMessages(ctx, c.ID, "", 10)
	for _, m := range msgs {
		if m.SenderID == "alice" && m.ReadAt != nil {
			t.Error("alice's own message should not get a read receipt from alice marking read")
		}
		if m.SenderID == "bob" && m.ReadAt == nil {
			t.Error("bob's message should be marked read after alice reads up to seq 2")
		}
	}
}

func TestDeleteMessage_MeVsEveryone(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	c, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	m, _, _ := r.InsertMessage(ctx, c.ID, "alice", "a1", "secret", "")

	if err := r.DeleteMessage(ctx, m.ID, "bob", false); err != nil {
		t.Fatal(err)
	}
	got, _ := r.GetMessage(ctx, m.ID)
	if !got.DeletedForMe["bob"] || got.Body != "secret" {
		t.Fatalf("expected tombstone-for-bob only, body preserved for others, got %+v", got)
	}

	if err := r.DeleteMessage(ctx, m.ID, "bob", true); err == nil {
		t.Fatal("expected non-sender delete-for-everyone to be rejected")
	}
	if err := r.DeleteMessage(ctx, m.ID, "alice", true); err != nil {
		t.Fatal(err)
	}
	got, _ = r.GetMessage(ctx, m.ID)
	if !got.DeletedForAll || got.Body != "" {
		t.Fatalf("expected delete-for-everyone to tombstone the body, got %+v", got)
	}
}

func TestListConversations_InboxVsRequests(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	active, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	pending, _ := r.GetOrCreateConversation(ctx, "alice", "carol", "carol", chat.StateRequest)

	inbox, _, _ := r.ListConversations(ctx, "alice", "inbox", "", 10)
	if len(inbox) != 1 || inbox[0].Conversation.ID != active.ID {
		t.Fatalf("expected only the active conversation in alice's inbox, got %+v", inbox)
	}

	requests, _, _ := r.ListConversations(ctx, "alice", "requests", "", 10)
	if len(requests) != 1 || requests[0].Conversation.ID != pending.ID {
		t.Fatalf("expected carol's pending request in alice's requests folder, got %+v", requests)
	}

	// carol (the initiator) should see her own pending request in her inbox, not her requests folder.
	carolInbox, _, _ := r.ListConversations(ctx, "carol", "inbox", "", 10)
	if len(carolInbox) != 1 || carolInbox[0].Conversation.ID != pending.ID {
		t.Fatalf("expected carol to see her own outgoing request in her inbox, got %+v", carolInbox)
	}
	carolRequests, _, _ := r.ListConversations(ctx, "carol", "requests", "", 10)
	if len(carolRequests) != 0 {
		t.Fatalf("carol's own outgoing request must not appear in her requests folder, got %+v", carolRequests)
	}
}

func TestListConversations_UnreadCount(t *testing.T) {
	r := chat.NewMemChatRepo()
	ctx := context.Background()
	c, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	r.InsertMessage(ctx, c.ID, "bob", "b1", "hi", "")
	r.InsertMessage(ctx, c.ID, "bob", "b2", "you there?", "")

	inbox, _, _ := r.ListConversations(ctx, "alice", "inbox", "", 10)
	if inbox[0].UnreadCount != 2 {
		t.Fatalf("expected 2 unread for alice, got %d", inbox[0].UnreadCount)
	}

	r.MarkRead(ctx, c.ID, "alice", 2)
	inbox, _, _ = r.ListConversations(ctx, "alice", "inbox", "", 10)
	if inbox[0].UnreadCount != 0 {
		t.Fatalf("expected 0 unread after marking read, got %d", inbox[0].UnreadCount)
	}
}
