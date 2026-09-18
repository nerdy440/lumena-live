package ws_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/lumena/gateway/bus"
	"github.com/lumena/gateway/ws"
)

func newTestServer(t *testing.T, b *bus.Bus, tokens *ws.TokenStore, isHost ws.IsHostFunc) *httptest.Server {
	t.Helper()
	h := ws.NewHandler(b, tokens, isHost, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, srv *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) bus.Event {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ev bus.Event
	if err := conn.ReadJSON(&ev); err != nil {
		t.Fatalf("read event: %v", err)
	}
	return ev
}

func alwaysHost(_ context.Context, _, _ string) (bool, error) { return true, nil }
func neverHost(_ context.Context, _, _ string) (bool, error) { return false, nil }

func TestWS_ConnectRejectsInvalidToken(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, alwaysHost)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?token=bogus"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("expected dial to fail for invalid token")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("expected 401, got resp=%v", resp)
	}
}

func TestWS_SubscribeAndReceiveComment(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, alwaysHost)

	tok := tokens.Issue("alice", "device-1")
	conn := dial(t, srv, tok)

	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	ack := readEvent(t, conn)
	if ack.Type != "SUBSCRIBED" || ack.Topic != "room:1" {
		t.Fatalf("expected SUBSCRIBED ack, got %+v", ack)
	}
	joined := readEvent(t, conn) // the subscriber's own VIEWER_JOINED, published right after the ack
	if joined.Type != "VIEWER_JOINED" {
		t.Fatalf("expected VIEWER_JOINED after SUBSCRIBED, got %+v", joined)
	}

	mustWrite(t, conn, map[string]any{
		"type": "COMMENT", "topic": "room:1",
		"payload": map[string]string{"body": "hello room", "lang": "en"},
	})

	ev := readEvent(t, conn)
	if ev.Type != "COMMENT" || ev.Topic != "room:1" || ev.Seq != 2 {
		t.Fatalf("expected COMMENT seq 2 on room:1 (seq 1 was VIEWER_JOINED), got %+v", ev)
	}
}

func TestWS_AutoSubscribesUserTopicAndReceivesModerationNotice(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, alwaysHost)

	hostTok := tokens.Issue("host-1", "device-1")
	hostConn := dial(t, srv, hostTok)
	mustWrite(t, hostConn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, hostConn) // SUBSCRIBED

	targetTok := tokens.Issue("target-1", "device-2")
	targetConn := dial(t, srv, targetTok)
	// target's first auto-delivered event is on user:target-1 (auto-subscribed on connect).

	mustWrite(t, hostConn, map[string]any{
		"type": "MUTE_USER", "topic": "room:1",
		"payload": map[string]any{"target_id": "target-1", "duration_s": 60, "rule_id": "CS-07"},
	})

	notice := readEvent(t, targetConn)
	if notice.Type != "ACCOUNT_STATUS" || notice.Topic != "user:target-1" {
		t.Fatalf("expected ACCOUNT_STATUS on user:target-1, got %+v", notice)
	}
	payload := notice.Payload.(map[string]any)
	if payload["rule_id"] != "CS-07" {
		t.Fatalf("expected rule_id CS-07 on enforcement event (rule_id must never be empty), got %+v", payload)
	}
}

func TestWS_NonHostCannotModerate(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, neverHost)

	tok := tokens.Issue("viewer-1", "device-1")
	conn := dial(t, srv, tok)

	mustWrite(t, conn, map[string]any{
		"type": "MUTE_USER", "topic": "room:1",
		"payload": map[string]any{"target_id": "someone"},
	})
	ev := readEvent(t, conn)
	if ev.Type != "ERROR" {
		t.Fatalf("expected ERROR for non-host moderation attempt, got %+v", ev)
	}
}

func TestWS_BackfillReplaysMissedEventsAfterReconnect(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, alwaysHost)

	tok1 := tokens.Issue("alice", "device-1")
	conn1 := dial(t, srv, tok1)
	mustWrite(t, conn1, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	sub := readEvent(t, conn1) // SUBSCRIBED, last_seq 0
	_ = conn1.Close()          // simulate disconnect

	// Events published while the client was disconnected.
	b.Publish("room:1", "COMMENT", map[string]string{"body": "missed 1"})
	b.Publish("room:1", "COMMENT", map[string]string{"body": "missed 2"})

	// Reconnect protocol (doc 08 §11): subscribe again, then request backfill from last known seq.
	tok2 := tokens.Issue("alice", "device-1")
	conn2 := dial(t, srv, tok2)
	mustWrite(t, conn2, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, conn2) // SUBSCRIBED
	_ = readEvent(t, conn2) // this reconnect's own VIEWER_JOINED

	payload := sub.Payload.(map[string]any)
	fromSeq := payload["last_seq"]
	mustWrite(t, conn2, map[string]any{
		"type": "BACKFILL", "topic": "room:1",
		"payload": map[string]any{"from_seq": fromSeq},
	})
	result := readEvent(t, conn2)
	if result.Type != "BACKFILL_RESULT" {
		t.Fatalf("expected BACKFILL_RESULT, got %+v", result)
	}
	events, _ := json.Marshal(result.Payload)
	if !strings.Contains(string(events), "missed 1") || !strings.Contains(string(events), "missed 2") {
		t.Fatalf("expected backfill to include both missed comments, got %s", events)
	}
}

func alwaysParticipant(_ context.Context, _, _ string) (bool, error) { return true, nil }
func neverParticipant(_ context.Context, _, _ string) (bool, error) { return false, nil }

func TestWS_PrivateChat_NonParticipantCannotSubscribe(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateChat(neverParticipant, nil, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("mallory", "device-1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "conv:1"})
	ev := readEvent(t, conn)
	if ev.Type != "ERROR" {
		t.Fatalf("expected ERROR for non-participant conv subscribe, got %+v", ev)
	}
}

// TestWS_CannotSubscribeToAnotherAccountsUserTopic is a regression test for
// a real leak: user:{id} carries an account's private events
// (BALANCE_CHANGED, DIAMONDS_CHANGED, ACCOUNT_STATUS, PV signaling,
// translated DMs, ...), but the SUBSCRIBE handler previously only
// authorization-checked conv: and pv: topics — any authenticated client
// could SUBSCRIBE to user:{victim} and silently observe another account's
// private events, including their balance after every gift.
func TestWS_CannotSubscribeToAnotherAccountsUserTopic(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	h := ws.NewHandler(b, tokens, alwaysHost, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("mallory", "device-1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "user:alice"})
	ev := readEvent(t, conn)
	if ev.Type != "ERROR" {
		t.Fatalf("expected ERROR subscribing to another account's user: topic, got %+v", ev)
	}

	// alice's own private events must never reach mallory's connection —
	// confirm nothing arrives even after a real event is published there.
	b.Publish("user:alice", "BALANCE_CHANGED", map[string]any{"coins": 999})
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var v map[string]any
	if err := conn.ReadJSON(&v); err == nil {
		t.Fatalf("mallory received an event on alice's private topic: %+v", v)
	}
}

func TestWS_PrivateChat_MessageDeliveredToBothParticipants(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()

	sent := map[string]bool{} // simulated idempotency, keyed by client_msg_id
	sendMessage := func(_ context.Context, senderID, convID, clientMsgID, body, attachmentID string) (map[string]any, error) {
		isNew := !sent[clientMsgID]
		sent[clientMsgID] = true
		return map[string]any{
			"msg_id": clientMsgID, "sender_id": senderID, "body": body,
			"seq": 1, "delivered_at": "now", "is_new": isNew,
		}, nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateChat(alwaysParticipant, sendMessage, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	aliceTok := tokens.Issue("alice", "d1")
	alice := dial(t, srv, aliceTok)
	bobTok := tokens.Issue("bob", "d2")
	bob := dial(t, srv, bobTok)

	mustWrite(t, alice, map[string]any{"type": "SUBSCRIBE", "topic": "conv:ab"})
	_ = readEvent(t, alice) // SUBSCRIBED
	mustWrite(t, bob, map[string]any{"type": "SUBSCRIBE", "topic": "conv:ab"})
	_ = readEvent(t, bob) // SUBSCRIBED

	mustWrite(t, alice, map[string]any{
		"type": "MESSAGE", "topic": "conv:ab",
		"payload": map[string]any{"body": "hi bob", "client_msg_id": "c1"},
	})

	aliceEv := readEvent(t, alice)
	bobEv := readEvent(t, bob)
	if aliceEv.Type != "MESSAGE" || bobEv.Type != "MESSAGE" {
		t.Fatalf("expected both participants to receive MESSAGE, got alice=%+v bob=%+v", aliceEv, bobEv)
	}
	bobPayload := bobEv.Payload.(map[string]any)
	if bobPayload["body"] != "hi bob" || bobPayload["sender_id"] != "alice" {
		t.Fatalf("unexpected message payload delivered to bob: %+v", bobPayload)
	}
}

func TestWS_PrivateChat_TypingAndReadRelayToParticipants(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	var markedRead []uint64
	markRead := func(_ context.Context, _, _ string, upToSeq uint64) error {
		markedRead = append(markedRead, upToSeq)
		return nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateChat(alwaysParticipant, nil, markRead)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	aliceTok := tokens.Issue("alice", "d1")
	alice := dial(t, srv, aliceTok)
	bobTok := tokens.Issue("bob", "d2")
	bob := dial(t, srv, bobTok)

	mustWrite(t, alice, map[string]any{"type": "SUBSCRIBE", "topic": "conv:ab"})
	_ = readEvent(t, alice)
	mustWrite(t, bob, map[string]any{"type": "SUBSCRIBE", "topic": "conv:ab"})
	_ = readEvent(t, bob)

	mustWrite(t, alice, map[string]any{"type": "TYPING", "topic": "conv:ab"})
	ev := readEvent(t, bob)
	if ev.Type != "TYPING" {
		t.Fatalf("expected bob to see alice TYPING, got %+v", ev)
	}
	_ = readEvent(t, alice) // alice also gets her own TYPING echo, since she's subscribed to the topic too

	mustWrite(t, bob, map[string]any{"type": "READ", "topic": "conv:ab", "payload": map[string]any{"up_to_seq": 5}})
	ev2 := readEvent(t, alice)
	if ev2.Type != "READ" {
		t.Fatalf("expected alice to see READ receipt, got %+v", ev2)
	}
	if len(markedRead) != 1 || markedRead[0] != 5 {
		t.Fatalf("expected MarkReadFunc called with up_to_seq=5, got %v", markedRead)
	}
}

type shortfallErr struct{ shortfall int64 }

func (e *shortfallErr) Error() string   { return "insufficient balance" }
func (e *shortfallErr) Shortfall() int64 { return e.shortfall }

func TestWS_Gift_SuccessBroadcastsToRoomAndPrivateBalanceEvents(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	sendGift := func(_ context.Context, senderID, recipientID, roomID, giftID string, quantity int64, idempotencyKey string) (map[string]any, error) {
		return map[string]any{
			"transaction_id": "tx-1", "gift_id": giftID, "gift_name": "Heart", "gift_asset_url": "/heart.json",
			"quantity": quantity, "coins_spent": int64(50), "new_sender_balance": int64(950),
			"creator_diamonds": int64(24), "new_recipient_diamonds": int64(24),
		}, nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithGifts(sendGift)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	senderTok := tokens.Issue("alice", "d1")
	sender := dial(t, srv, senderTok)
	recipientTok := tokens.Issue("bob", "d2")
	recipient := dial(t, srv, recipientTok) // auto-subscribed to user:bob on connect

	mustWrite(t, sender, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, sender) // SUBSCRIBED
	_ = readEvent(t, sender) // sender's own VIEWER_JOINED

	mustWrite(t, sender, map[string]any{
		"type": "GIFT_SENT", "topic": "room:1",
		"payload": map[string]any{"recipient_id": "bob", "gift_id": "g_heart", "quantity": 1, "idempotency_key": "k1"},
	})

	// GIFT_SENT (room:1) and BALANCE_CHANGED (user:alice) are published to
	// two different topics with independent subscriber channels — same-
	// topic delivery order is guaranteed (via seq), but there's no
	// guarantee across topics, so the sender may receive these two in
	// either order.
	var roomEv, balanceEv bus.Event
	for i := 0; i < 2; i++ {
		ev := readEvent(t, sender)
		switch ev.Type {
		case "GIFT_SENT":
			roomEv = ev
		case "BALANCE_CHANGED":
			balanceEv = ev
		default:
			t.Fatalf("unexpected event type %+v", ev)
		}
	}
	if roomEv.Topic != "room:1" {
		t.Fatalf("expected GIFT_SENT on room:1, got %+v", roomEv)
	}
	roomPayload := roomEv.Payload.(map[string]any)
	if roomPayload["sender_id"] != "alice" || roomPayload["recipient_id"] != "bob" {
		t.Fatalf("unexpected GIFT_SENT payload: %+v", roomPayload)
	}
	if balanceEv.Topic != "user:alice" {
		t.Fatalf("expected private BALANCE_CHANGED to sender, got %+v", balanceEv)
	}
	balPayload := balanceEv.Payload.(map[string]any)
	if balPayload["new_balance"] != float64(950) {
		t.Fatalf("expected new_balance 950, got %+v", balPayload)
	}

	diamondEv := readEvent(t, recipient)
	if diamondEv.Type != "DIAMONDS_CHANGED" || diamondEv.Topic != "user:bob" {
		t.Fatalf("expected private DIAMONDS_CHANGED to recipient, got %+v", diamondEv)
	}
}

func TestWS_Gift_InsufficientBalanceReturnsShortfallWithoutBroadcast(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	sendGift := func(context.Context, string, string, string, string, int64, string) (map[string]any, error) {
		return nil, &shortfallErr{shortfall: 40}
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithGifts(sendGift)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("alice", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, conn) // SUBSCRIBED
	_ = readEvent(t, conn) // VIEWER_JOINED

	mustWrite(t, conn, map[string]any{
		"type": "GIFT_SENT", "topic": "room:1",
		"payload": map[string]any{"recipient_id": "bob", "gift_id": "g_heart", "quantity": 1, "idempotency_key": "k1"},
	})
	ev := readEvent(t, conn)
	if ev.Type != "ERROR" {
		t.Fatalf("expected ERROR, got %+v", ev)
	}
	payload := ev.Payload.(map[string]any)
	if payload["code"] != "INSUFFICIENT_BALANCE" || payload["shortfall"] != float64(40) {
		t.Fatalf("expected INSUFFICIENT_BALANCE with shortfall 40, got %+v", payload)
	}
}

func TestWS_PV_NonParticipantCannotSubscribe(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateVideo(neverParticipant, nil, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("mallory", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "pv:s1"})
	ev := readEvent(t, conn)
	if ev.Type != "ERROR" {
		t.Fatalf("expected ERROR for non-participant pv subscribe, got %+v", ev)
	}
}

func TestWS_PV_SDPRelayAndConnectedHook(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	var connectedCalls []string
	onConnected := func(_ context.Context, sessionID string) error {
		connectedCalls = append(connectedCalls, sessionID)
		return nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateVideo(alwaysParticipant, onConnected, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	callerTok := tokens.Issue("alice", "d1")
	caller := dial(t, srv, callerTok)
	calleeTok := tokens.Issue("bob", "d2")
	callee := dial(t, srv, calleeTok)

	mustWrite(t, caller, map[string]any{"type": "SUBSCRIBE", "topic": "pv:s1"})
	_ = readEvent(t, caller)
	mustWrite(t, callee, map[string]any{"type": "SUBSCRIBE", "topic": "pv:s1"})
	_ = readEvent(t, callee)

	mustWrite(t, caller, map[string]any{"type": "SDP_OFFER", "topic": "pv:s1", "payload": map[string]any{"sdp": "offer-data"}})
	ev := readEvent(t, callee)
	if ev.Type != "SDP_OFFER" {
		t.Fatalf("expected callee to receive relayed SDP_OFFER, got %+v", ev)
	}
	_ = readEvent(t, caller) // caller's own SDP_OFFER self-echo — pv:s1 is a shared topic both sides subscribed to

	mustWrite(t, callee, map[string]any{"type": "SDP_ANSWER", "topic": "pv:s1", "payload": map[string]any{"sdp": "answer-data"}})
	ev2 := readEvent(t, caller)
	if ev2.Type != "SDP_ANSWER" {
		t.Fatalf("expected caller to receive relayed SDP_ANSWER, got %+v", ev2)
	}

	if len(connectedCalls) != 1 || connectedCalls[0] != "s1" {
		t.Fatalf("expected onPVConnected called once for session s1, got %v", connectedCalls)
	}
}

func TestWS_PV_DisconnectHookFiresOnUnexpectedClose(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	disconnected := make(chan string, 1)
	onDisconnected := func(_ context.Context, sessionID string) error {
		disconnected <- sessionID
		return nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithPrivateVideo(alwaysParticipant, nil, onDisconnected)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("alice", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "pv:s1"})
	_ = readEvent(t, conn)
	conn.Close()

	select {
	case sessionID := <-disconnected:
		if sessionID != "s1" {
			t.Fatalf("expected disconnect hook for s1, got %s", sessionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected onPVDisconnected to fire after the connection closed")
	}
}

func TestWS_Translation_DeliveredPrivatelyAndDoesNotBlockOriginalComment(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	translateFn := func(_ context.Context, body, sourceLang, targetLang string) (string, float64, error) {
		return "[MOCK: " + body + "]", 0.99, nil
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithTranslation(translateFn)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("alice", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, conn) // SUBSCRIBED
	_ = readEvent(t, conn) // VIEWER_JOINED (self)

	mustWrite(t, conn, map[string]any{
		"type": "COMMENT", "topic": "room:1",
		"payload": map[string]string{"body": "hello", "lang": "en"},
	})
	comment := readEvent(t, conn)
	if comment.Type != "COMMENT" {
		t.Fatalf("expected COMMENT to arrive immediately (never delayed by translation), got %+v", comment)
	}

	mustWrite(t, conn, map[string]any{
		"type": "REQUEST_TRANSLATION", "topic": "room:1",
		"payload": map[string]any{"ref_msg_id": "m1", "body": "hello", "source_lang": "en", "target_lang": "ar"},
	})
	translated := readEvent(t, conn)
	if translated.Type != "COMMENT_TRANSLATED" {
		t.Fatalf("expected COMMENT_TRANSLATED, got %+v", translated)
	}
	if translated.Topic != "user:alice" {
		t.Fatalf("expected translation delivered on the requester's private user topic, got %q", translated.Topic)
	}
	payload := translated.Payload.(map[string]any)
	if payload["ref_msg_id"] != "m1" || payload["translated_body"] != "[MOCK: hello]" || payload["target_lang"] != "ar" {
		t.Fatalf("unexpected translation payload: %+v", payload)
	}
}

func TestWS_Translation_ProviderFailureIsSilentNotAnError(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	translateFn := func(_ context.Context, body, sourceLang, targetLang string) (string, float64, error) {
		return "", 0, errors.New("vendor unavailable")
	}
	h := ws.NewHandler(b, tokens, alwaysHost, nil).WithTranslation(translateFn)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("alice", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, conn) // SUBSCRIBED
	_ = readEvent(t, conn) // VIEWER_JOINED

	mustWrite(t, conn, map[string]any{
		"type": "REQUEST_TRANSLATION", "topic": "room:1",
		"payload": map[string]any{"ref_msg_id": "m1", "body": "hello", "source_lang": "en", "target_lang": "ar"},
	})
	// Nothing should ever arrive for a failed translation — not even an
	// ERROR event, since translation absence must never surface as a
	// chat-pipeline error (doc 08 §5).
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var v map[string]any
	err := conn.ReadJSON(&v)
	if err == nil {
		t.Fatalf("expected no event for a failed translation, got %+v", v)
	}
}

func TestWS_Translation_NotWiredIsANoOp(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	h := ws.NewHandler(b, tokens, alwaysHost, nil) // WithTranslation never called
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	tok := tokens.Issue("alice", "d1")
	conn := dial(t, srv, tok)
	mustWrite(t, conn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, conn)
	_ = readEvent(t, conn)

	mustWrite(t, conn, map[string]any{
		"type": "REQUEST_TRANSLATION", "topic": "room:1",
		"payload": map[string]any{"ref_msg_id": "m1", "body": "hello", "source_lang": "en", "target_lang": "ar"},
	})
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var v map[string]any
	err := conn.ReadJSON(&v)
	if err == nil {
		t.Fatalf("expected no event when translation isn't wired up, got %+v", v)
	}
}

func mustWrite(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}
