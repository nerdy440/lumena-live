package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/chat"
	"github.com/lumena/chat/pgrepo"
	"github.com/lumena/db"
)

func newTestRepo(t *testing.T) (*pgrepo.Repo, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.New(pool), pool
}

func TestGetOrCreateConversation_OneRowPerPairRegardlessOfOrder(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	c1, err := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateRequest)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c1.ParticipantA != "alice" || c1.ParticipantB != "bob" {
		t.Fatalf("expected lexicographic order, got %+v", c1)
	}

	c2, err := r.GetOrCreateConversation(ctx, "bob", "alice", "bob", chat.StateActive)
	if err != nil {
		t.Fatalf("get existing: %v", err)
	}
	if c2.ID != c1.ID || c2.State != chat.StateRequest {
		t.Fatalf("expected the same existing conversation untouched, got %+v", c2)
	}
}

func TestInsertMessage_IdempotentOnClientMsgID(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	conv, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)

	m1, isNew1, err := r.InsertMessage(ctx, conv.ID, "alice", "client-1", "hi", "")
	if err != nil || !isNew1 {
		t.Fatalf("first insert: msg=%+v isNew=%v err=%v", m1, isNew1, err)
	}
	m2, isNew2, err := r.InsertMessage(ctx, conv.ID, "alice", "client-1", "hi again — should be ignored", "")
	if err != nil || isNew2 {
		t.Fatalf("retry: expected isNew=false, got msg=%+v isNew=%v err=%v", m2, isNew2, err)
	}
	if m1.ID != m2.ID || m2.Body != "hi" {
		t.Fatalf("retry must return the ORIGINAL message: m1=%+v m2=%+v", m1, m2)
	}
}

func TestInsertMessage_SeqIncrementsPerConversation(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	conv, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)

	m1, _, _ := r.InsertMessage(ctx, conv.ID, "alice", "c1", "one", "")
	m2, _, _ := r.InsertMessage(ctx, conv.ID, "bob", "c2", "two", "")
	m3, _, _ := r.InsertMessage(ctx, conv.ID, "alice", "c3", "three", "")
	if m1.Seq != 1 || m2.Seq != 2 || m3.Seq != 3 {
		t.Fatalf("expected seq 1,2,3 got %d,%d,%d", m1.Seq, m2.Seq, m3.Seq)
	}
}

func TestListConversations_InboxVsRequestsFolders(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	// alice -> bob: a pending request alice sent (shows in alice's inbox, bob's requests)
	_, _ = r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateRequest)
	// alice <-> carol: already active
	_, _ = r.GetOrCreateConversation(ctx, "alice", "carol", "carol", chat.StateActive)

	aliceInbox, _, err := r.ListConversations(ctx, "alice", "inbox", "", 10)
	if err != nil {
		t.Fatalf("alice inbox: %v", err)
	}
	if len(aliceInbox) != 2 {
		t.Fatalf("alice inbox should show both her own request and the active convo, got %d: %+v", len(aliceInbox), aliceInbox)
	}

	bobRequests, _, err := r.ListConversations(ctx, "bob", "requests", "", 10)
	if err != nil {
		t.Fatalf("bob requests: %v", err)
	}
	if len(bobRequests) != 1 {
		t.Fatalf("bob should see exactly 1 incoming request, got %d: %+v", len(bobRequests), bobRequests)
	}

	bobInbox, _, err := r.ListConversations(ctx, "bob", "inbox", "", 10)
	if err != nil {
		t.Fatalf("bob inbox: %v", err)
	}
	if len(bobInbox) != 0 {
		t.Fatalf("bob's inbox should NOT show alice's outgoing request, got %+v", bobInbox)
	}
}

func TestListConversations_MostRecentlyActiveFirst(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	convAB, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	convAC, _ := r.GetOrCreateConversation(ctx, "alice", "carol", "alice", chat.StateActive)
	// Touch AB after AC was created, so AB should now sort first.
	_, _, _ = r.InsertMessage(ctx, convAB.ID, "alice", "c1", "hey bob", "")

	list, _, err := r.ListConversations(ctx, "alice", "inbox", "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Conversation.ID != convAB.ID {
		t.Fatalf("expected AB conversation first (most recently touched), got %+v want first=%s convAC=%s", list, convAB.ID, convAC.ID)
	}
}

func TestMarkRead_OnlyAffectsOtherSendersUpToSeq(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	conv, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	m1, _, _ := r.InsertMessage(ctx, conv.ID, "alice", "c1", "one", "")
	_, _, _ = r.InsertMessage(ctx, conv.ID, "bob", "c2", "own message, must stay unread=n/a", "")
	_, _, _ = r.InsertMessage(ctx, conv.ID, "alice", "c3", "three", "")

	if err := r.MarkRead(ctx, conv.ID, "bob", m1.Seq); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	got1, _ := r.GetMessage(ctx, m1.ID)
	if got1.ReadAt == nil {
		t.Fatalf("message 1 should now be read")
	}

	summaries, _, err := r.ListConversations(ctx, "bob", "inbox", "", 10)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("list: %+v err=%v", summaries, err)
	}
	if summaries[0].UnreadCount != 1 {
		t.Fatalf("bob should have exactly 1 unread (message 3, since message1 now read and message2 is his own), got %d", summaries[0].UnreadCount)
	}
}

func TestDeleteMessage_ForMeVsForEveryone(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	conv, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	m1, _, _ := r.InsertMessage(ctx, conv.ID, "alice", "c1", "secret", "")

	// Bob can't delete-for-everyone alice's message.
	if err := r.DeleteMessage(ctx, m1.ID, "bob", true); !errors.Is(err, chat.ErrNotParticipant) {
		t.Fatalf("expected ErrNotParticipant, got %v", err)
	}

	// Bob deletes it for himself only — body must stay intact for alice.
	if err := r.DeleteMessage(ctx, m1.ID, "bob", false); err != nil {
		t.Fatalf("delete for me: %v", err)
	}
	stillThere, err := r.GetMessage(ctx, m1.ID)
	if err != nil || stillThere.Body != "secret" || stillThere.DeletedForAll {
		t.Fatalf("delete-for-me must not affect the message globally: %+v err=%v", stillThere, err)
	}

	// Alice (the sender) deletes it for everyone — body is wiped.
	if err := r.DeleteMessage(ctx, m1.ID, "alice", true); err != nil {
		t.Fatalf("delete for everyone: %v", err)
	}
	gone, err := r.GetMessage(ctx, m1.ID)
	if err != nil || !gone.DeletedForAll || gone.Body != "" {
		t.Fatalf("expected wiped body + deleted_for_all, got %+v err=%v", gone, err)
	}
}

func TestListMessages_Pagination(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	conv, _ := r.GetOrCreateConversation(ctx, "alice", "bob", "alice", chat.StateActive)
	for i := 0; i < 3; i++ {
		_, _, _ = r.InsertMessage(ctx, conv.ID, "alice", "c"+string(rune('1'+i)), "msg", "")
	}

	page1, cursor, err := r.ListMessages(ctx, conv.ID, "", 2)
	if err != nil || len(page1) != 2 || cursor == "" {
		t.Fatalf("page1: %+v cursor=%q err=%v", page1, cursor, err)
	}
	page2, cursor2, err := r.ListMessages(ctx, conv.ID, cursor, 2)
	if err != nil || len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2: %+v cursor=%q err=%v", page2, cursor2, err)
	}
}
