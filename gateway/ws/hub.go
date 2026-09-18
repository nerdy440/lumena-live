// Package ws implements the realtime WebSocket gateway from doc 08:
// envelope, subscription management, backfill, and the room/user/sys
// event topics. It fans events out via gateway/bus, which stands in for
// the Kafka-backed bus described in doc 08 §12 (see that package's doc
// comment).
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/lumena/gateway/bus"
	"github.com/lumena/gateway/internal/topics"
)

const (
	// clientPingInterval / pongWait implement doc 08 §1's heartbeat: the
	// client is expected to ping every 25s; if we hear nothing (ping or any
	// other frame) for pongWait we consider the connection dead.
	pongWait = 40 * time.Second
	// serverPingInterval is how often the server itself pings the client.
	serverPingInterval = 30 * time.Second
	writeWait          = 5 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // dev: no cross-origin restriction
}

// IsHostFunc reports whether accountID is the host of roomID. It exists so
// this package does not need to depend on the feed module — main.go wires
// a closure over feedsvc.Service.GetRoom.
type IsHostFunc func(ctx context.Context, roomID, accountID string) (bool, error)

// IsParticipantFunc reports whether accountID is a party to conversationID.
// Only participants may subscribe to or post on a conv:{id} topic — this is
// what keeps a private conversation private. main.go wires a closure over
// chatsvc via chat.Repo.GetConversation.
type IsParticipantFunc func(ctx context.Context, conversationID, accountID string) (bool, error)

// SendPrivateMessageFunc persists a direct message and returns the fields
// to publish on the conv:{id} topic (doc 08 §9's MESSAGE payload). Returning
// a plain map keeps this package free of a dependency on the chat module.
type SendPrivateMessageFunc func(ctx context.Context, senderID, conversationID, clientMsgID, body, attachmentID string) (payload map[string]any, err error)

// MarkReadFunc persists a read-receipt cursor for a conversation.
type MarkReadFunc func(ctx context.Context, viewerID, conversationID string, upToSeq uint64) error

// SendGiftFunc posts a gift transaction and returns the fields needed for
// both the room-scoped GIFT_SENT broadcast and the two private balance
// events (doc 08 §6). Returning a plain map keeps this package free of a
// dependency on the ledger module — main.go wires a closure over
// giftsvc.Service.SendGift.
type SendGiftFunc func(ctx context.Context, senderID, recipientID, roomID, giftID string, quantity int64, idempotencyKey string) (payload map[string]any, err error)

// shortfallError is implemented by ledger.InsufficientBalanceError. Declared
// locally (structural typing via errors.As) so this package can surface the
// exact shortfall to the client without importing the ledger module.
type shortfallError interface {
	Shortfall() int64
}

// Handler upgrades HTTP connections to the realtime WebSocket protocol.
type Handler struct {
	bus           *bus.Bus
	tokens        *TokenStore
	isHost        IsHostFunc
	isParticipant IsParticipantFunc
	sendMessage   SendPrivateMessageFunc
	markRead      MarkReadFunc
	sendGift      SendGiftFunc
	isPVParticipant IsPVParticipantFunc
	onPVConnected   PVConnectedFunc
	onPVDisconnected PVDisconnectedFunc
	translate     TranslateFunc
	log           *slog.Logger

	updateViewerCount UpdateViewerCountFunc
	roomViewersMu     sync.Mutex
	roomViewers       map[string]int
}

func NewHandler(b *bus.Bus, tokens *TokenStore, isHost IsHostFunc, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{bus: b, tokens: tokens, isHost: isHost, log: log, roomViewers: make(map[string]int)}
}

// WithPrivateChat wires the doc 08 §9 conv:{id} topic (private 1-to-1
// messaging) into the gateway. Without calling this, MESSAGE/TYPING/READ on
// a conv: topic are rejected — the gateway can run Phase 6 (rooms only)
// without Phase 7's chat module present.
func (h *Handler) WithPrivateChat(isParticipant IsParticipantFunc, sendMessage SendPrivateMessageFunc, markRead MarkReadFunc) *Handler {
	h.isParticipant = isParticipant
	h.sendMessage = sendMessage
	h.markRead = markRead
	return h
}

// WithGifts wires the doc 08 §6 economic events into the gateway. Without
// calling this, GIFT_SENT on a room topic is rejected — Phase 6 can run
// without Phase 8's ledger module present.
func (h *Handler) WithGifts(sendGift SendGiftFunc) *Handler {
	h.sendGift = sendGift
	return h
}

// IsPVParticipantFunc reports whether accountID is a party to PV session
// sessionID — only the caller and callee may subscribe to or post on its
// pv:{id} topic.
type IsPVParticipantFunc func(ctx context.Context, sessionID, accountID string) (bool, error)

// PVConnectedFunc/PVDisconnectedFunc drive billing interval tracking (doc
// 09 §5). This dev build has no real ICE/media stack to observe a
// "connected" state from, so the gateway treats a completed SDP answer
// relay as that signal — see pv package's doc comment for why.
type PVConnectedFunc func(ctx context.Context, sessionID string) error
type PVDisconnectedFunc func(ctx context.Context, sessionID string) error

// WithPrivateVideo wires the doc 08 §8 pv:{id} topic: SDP/ICE signaling
// relay plus the connect/disconnect hooks that drive billing intervals.
func (h *Handler) WithPrivateVideo(isParticipant IsPVParticipantFunc, onConnected PVConnectedFunc, onDisconnected PVDisconnectedFunc) *Handler {
	h.isPVParticipant = isParticipant
	h.onPVConnected = onConnected
	h.onPVDisconnected = onDisconnected
	return h
}

// TranslateFunc translates text — main.go wires a closure over
// translate.Provider so this package never imports the translate module
// directly. Implementations may be slow or fail; callers here always
// invoke it off the read loop (doc 08 §5: translation must never block or
// delay original message delivery).
type TranslateFunc func(ctx context.Context, body, sourceLang, targetLang string) (translatedBody string, confidence float64, err error)

// WithTranslation wires the doc 08 §5 COMMENT_TRANSLATED flow. Without
// calling this, REQUEST_TRANSLATION is silently a no-op — translation
// failure/absence must never surface as a chat-pipeline error.
func (h *Handler) WithTranslation(translateFn TranslateFunc) *Handler {
	h.translate = translateFn
	return h
}

// UpdateViewerCountFunc persists a room's live viewer count — adapts
// feedsvc so a room's denormalized viewer_count (doc 06's rooms table)
// actually reflects who's connected, instead of staying frozen at
// whatever it was when the room was created. Optional: without it, the
// in-memory count below still drives the VIEWER_JOINED/VIEWER_LEFT
// payloads' viewer_count field, just without persisting to REST reads.
type UpdateViewerCountFunc func(ctx context.Context, roomID string, count int)

// WithViewerCount wires live viewer-count tracking.
func (h *Handler) WithViewerCount(fn UpdateViewerCountFunc) *Handler {
	h.updateViewerCount = fn
	return h
}

// viewerJoined/viewerLeft maintain an in-process count per room — the
// single source of truth for "how many WS connections currently hold a
// room: subscription," which is the actual definition of "viewer count"
// doc 03's room card promises. Every SUBSCRIBE/UNSUBSCRIBE and every
// ungraceful disconnect (closeAll) goes through one of these, so the
// count can never drift from the real subscriber set.
func (h *Handler) viewerJoined(ctx context.Context, roomID string) int {
	h.roomViewersMu.Lock()
	h.roomViewers[roomID]++
	count := h.roomViewers[roomID]
	h.roomViewersMu.Unlock()
	if h.updateViewerCount != nil {
		h.updateViewerCount(ctx, roomID, count)
	}
	return count
}

func (h *Handler) viewerLeft(ctx context.Context, roomID string) int {
	h.roomViewersMu.Lock()
	if h.roomViewers[roomID] > 0 {
		h.roomViewers[roomID]--
	}
	count := h.roomViewers[roomID]
	h.roomViewersMu.Unlock()
	if h.updateViewerCount != nil {
		h.updateViewerCount(ctx, roomID, count)
	}
	return count
}

// currentViewerCount reads the count without mutating it — used when the
// subscriber joining/leaving is the room's own host, who is deliberately
// excluded from their own viewer count (matching the reference product:
// the person broadcasting isn't one of their own viewers) but whose
// VIEWER_JOINED/VIEWER_LEFT payload still needs an accurate count field.
func (h *Handler) currentViewerCount(roomID string) int {
	h.roomViewersMu.Lock()
	defer h.roomViewersMu.Unlock()
	return h.roomViewers[roomID]
}

// clientEnvelope mirrors doc 08 §2, using client_seq instead of seq.
type clientEnvelope struct {
	MsgID     string          `json:"msg_id"`
	Type      string          `json:"type"`
	Topic     string          `json:"topic"`
	ClientSeq uint64          `json:"client_seq,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// ServeHTTP handles GET /ws?token=...&device=... (doc 08 §1). The token is
// the short-lived single-use ws_token minted by /api/v1/auth/ws-token, not
// the long-lived access token — see TokenStore's doc comment for why.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	accountID, deviceID, ok := h.tokens.Consume(tok)
	if !ok {
		http.Error(w, `{"error":{"code":"UNAUTHENTICATED","message":"invalid, expired, or already-used ws token"}}`, http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", "err", err)
		return
	}

	c := &client{
		conn:          conn,
		hub:           h,
		bus:           h.bus,
		isHost:        h.isHost,
		isParticipant: h.isParticipant,
		sendMessage:   h.sendMessage,
		markRead:      h.markRead,
		sendGift:      h.sendGift,
		isPVParticipant: h.isPVParticipant,
		onPVConnected:   h.onPVConnected,
		onPVDisconnected: h.onPVDisconnected,
		translate:     h.translate,
		accountID:     accountID,
		deviceID:      deviceID,
		send:          make(chan bus.Event, 128),
		subs:          make(map[string]func()),
		log:           h.log,
	}
	c.run(r.Context())
}

// client is one live WebSocket connection and its topic subscriptions.
type client struct {
	conn          *websocket.Conn
	hub           *Handler
	bus           *bus.Bus
	isHost        IsHostFunc
	isParticipant IsParticipantFunc
	sendMessage   SendPrivateMessageFunc
	markRead      MarkReadFunc
	sendGift      SendGiftFunc
	isPVParticipant IsPVParticipantFunc
	onPVConnected   PVConnectedFunc
	onPVDisconnected PVDisconnectedFunc
	translate     TranslateFunc
	accountID     string
	deviceID      string

	send chan bus.Event
	subs map[string]func() // topic -> unsubscribe
	log  *slog.Logger
}

func (c *client) run(ctx context.Context) {
	defer c.closeAll()

	// Automatic subscriptions on connect (doc 08 §3).
	c.subscribe(topics.User(c.accountID))
	c.subscribe(topics.Sys)

	done := make(chan struct{})
	go c.writePump(done)
	c.readPump(ctx)
	close(done)
}

// viewerJoinedRoom/viewerLeftRoom are the SUBSCRIBE/UNSUBSCRIBE(+disconnect)
// shared paths — they mutate the count unless this connection belongs to
// the room's own host, who is deliberately excluded from their own
// viewer count (matching the reference product: the broadcaster isn't
// one of their own viewers).
func (c *client) viewerJoinedRoom(ctx context.Context, roomID string) int {
	if isHost, _ := c.isHost(ctx, roomID, c.accountID); isHost {
		return c.hub.currentViewerCount(roomID)
	}
	return c.hub.viewerJoined(ctx, roomID)
}

func (c *client) viewerLeftRoom(ctx context.Context, roomID string) int {
	if isHost, _ := c.isHost(ctx, roomID, c.accountID); isHost {
		return c.hub.currentViewerCount(roomID)
	}
	return c.hub.viewerLeft(ctx, roomID)
}

func (c *client) closeAll() {
	for topic, unsub := range c.subs {
		unsub()
		if roomID, ok := topics.IsRoom(topic); ok {
			count := c.viewerLeftRoom(context.Background(), roomID)
			c.bus.Publish(topic, "VIEWER_LEFT", map[string]any{"user_id": c.accountID, "room_id": roomID, "viewer_count": count})
		}
		// An unexpected drop while on a pv: topic is exactly the disconnect
		// billing intervals need to close on — use a background context so
		// this still runs even if the connection's own context is done.
		if sessionID, ok := topics.IsPV(topic); ok && c.onPVDisconnected != nil {
			_ = c.onPVDisconnected(context.Background(), sessionID)
		}
	}
	_ = c.conn.Close()
}

// subscribe registers the bus subscription only — it deliberately does not
// publish VIEWER_JOINED, so callers can send the SUBSCRIBED ack first and
// announce the join afterward. Without that ordering, the join event (fanned
// out asynchronously by forward) can race the ack over the same send
// channel and arrive before it.
func (c *client) subscribe(topic string) uint64 {
	if _, already := c.subs[topic]; already {
		return c.bus.LastSeq(topic)
	}
	ch, lastSeq, unsub := c.bus.Subscribe(topic)
	c.subs[topic] = unsub
	go c.forward(ch)
	return lastSeq
}

func (c *client) unsubscribe(topic string) {
	unsub, ok := c.subs[topic]
	if !ok {
		return
	}
	unsub()
	delete(c.subs, topic)
	if roomID, ok := topics.IsRoom(topic); ok {
		count := c.viewerLeftRoom(context.Background(), roomID)
		c.bus.Publish(topic, "VIEWER_LEFT", map[string]any{"user_id": c.accountID, "room_id": roomID, "viewer_count": count})
	}
}

// forward relays bus events for one subscription onto the connection's
// shared send channel until the subscription's channel is closed/replaced.
func (c *client) forward(ch <-chan bus.Event) {
	for ev := range ch {
		select {
		case c.send <- ev:
		default:
			// send buffer full: drop rather than block the whole bus fan-out.
			// The client's own reconnect+backfill logic (doc 08 §11) recovers.
		}
	}
}

func (c *client) writePump(done <-chan struct{}) {
	ticker := time.NewTicker(serverPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case ev := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(ev); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) readPump(ctx context.Context) {
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		var env clientEnvelope
		if err := c.conn.ReadJSON(&env); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		c.handle(ctx, env)
	}
}

func (c *client) handle(ctx context.Context, env clientEnvelope) {
	switch env.Type {
	case "PING":
		c.sendControl("PONG", env.Topic, nil)

	case "SUBSCRIBE":
		if convID, isConv := topics.IsConv(env.Topic); isConv && !c.authorizedForConv(ctx, convID) {
			c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
			return
		}
		if sessionID, isPV := topics.IsPV(env.Topic); isPV && !c.authorizedForPV(ctx, sessionID) {
			c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
			return
		}
		if userID, isUser := topics.IsUser(env.Topic); isUser && !c.authorizedForUser(userID) {
			c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
			return
		}
		lastSeq := c.subscribe(env.Topic)
		c.sendControl("SUBSCRIBED", env.Topic, map[string]any{"last_seq": lastSeq})
		if roomID, ok := topics.IsRoom(env.Topic); ok {
			count := c.viewerJoinedRoom(ctx, roomID)
			c.bus.Publish(env.Topic, "VIEWER_JOINED", map[string]any{"user_id": c.accountID, "room_id": roomID, "viewer_count": count})
		}

	case "UNSUBSCRIBE":
		c.unsubscribe(env.Topic)

	case "BACKFILL":
		var p struct {
			FromSeq uint64 `json:"from_seq"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		events, ok := c.bus.Backfill(env.Topic, p.FromSeq)
		if !ok {
			c.sendControl("CATCHUP_REQUIRED", env.Topic, nil)
			return
		}
		c.sendControl("BACKFILL_RESULT", env.Topic, map[string]any{"events": events})

	case "COMMENT":
		c.publishComment(env)

	case "LIKE":
		roomID, isRoom := topics.IsRoom(env.Topic)
		if !isRoom {
			return
		}
		c.bus.Publish(env.Topic, "LIKE", map[string]any{"user_id": c.accountID, "room_id": roomID})

	case "GIFT_SENT":
		c.handleGift(ctx, env)

	case "MUTE_USER", "KICK_USER":
		c.handleModeration(ctx, env)

	case "MESSAGE":
		c.handlePrivateMessage(ctx, env)

	case "TYPING":
		if convID, isConv := topics.IsConv(env.Topic); isConv && c.authorizedForConv(ctx, convID) {
			c.bus.Publish(env.Topic, "TYPING", map[string]any{"user_id": c.accountID})
		}

	case "READ":
		c.handleReadReceipt(ctx, env)

	case "SDP_OFFER", "ICE_CANDIDATE":
		c.relayPV(ctx, env)

	case "SDP_ANSWER":
		c.relayPV(ctx, env)
		// Dev-build billing signal — see PVConnectedFunc's doc comment:
		// there's no real ICE/media stack here, so a completed SDP answer
		// is treated as "both peers connected."
		if sessionID, isPV := topics.IsPV(env.Topic); isPV && c.onPVConnected != nil {
			_ = c.onPVConnected(ctx, sessionID)
		}

	case "BROADCAST_SIGNAL":
		c.relayBroadcastSignal(env)

	case "REQUEST_TRANSLATION":
		c.handleTranslationRequest(env)

	default:
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "UNKNOWN_TYPE", "topic": env.Topic})
	}
}

// relayBroadcastSignal is a point-to-point WebRTC signaling relay for room
// broadcast video (host camera -> viewer playback), addressed by
// target_account_id rather than a shared topic — a room can have any
// number of viewers, each needing its own independent SDP/ICE exchange
// with the host, unlike private video's fixed 2-party pv:{id} session.
// Delivery rides the target's own user:{id} topic, which every connected
// client is already auto-subscribed to (doc 08 §3).
func (c *client) relayBroadcastSignal(env clientEnvelope) {
	var p map[string]any
	_ = json.Unmarshal(env.Payload, &p)
	if p == nil {
		return
	}
	targetID, _ := p["target_account_id"].(string)
	if targetID == "" {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "VALIDATION_ERROR", "topic": env.Topic})
		return
	}
	p["sender_id"] = c.accountID
	c.bus.Publish(topics.User(targetID), "BROADCAST_SIGNAL", p)
}

// handleTranslationRequest implements doc 08 §5's async translation: a
// viewer asks for one comment translated into their own target language.
// Delivered privately to the requester's own user:{id} topic, not
// broadcast to the room — different viewers may want different target
// languages, so there is no single "the room's translation" to fan out.
// Runs off the read loop entirely: a slow or failing provider can never
// block or delay the original COMMENT, which has already been delivered
// by the time this fires.
func (c *client) handleTranslationRequest(env clientEnvelope) {
	if c.translate == nil {
		return // translation not configured — never surfaced as an error
	}
	if _, isRoom := topics.IsRoom(env.Topic); !isRoom {
		return
	}
	var p struct {
		RefMsgID   string `json:"ref_msg_id"`
		Body       string `json:"body"`
		SourceLang string `json:"source_lang"`
		TargetLang string `json:"target_lang"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if p.Body == "" || p.TargetLang == "" {
		return
	}
	requesterID := c.accountID
	translateFn := c.translate
	bus := c.bus
	go func() {
		translated, confidence, err := translateFn(context.Background(), p.Body, p.SourceLang, p.TargetLang)
		if err != nil {
			return
		}
		bus.Publish(topics.User(requesterID), "COMMENT_TRANSLATED", map[string]any{
			"ref_msg_id": p.RefMsgID, "translated_body": translated,
			"target_lang": p.TargetLang, "confidence": confidence,
		})
	}()
}

func (c *client) publishComment(env clientEnvelope) {
	roomID, isRoom := topics.IsRoom(env.Topic)
	if !isRoom {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "TOPIC_NOT_FOUND", "topic": env.Topic})
		return
	}
	var p struct {
		Body string `json:"body"`
		Lang string `json:"lang"`
	}
	_ = json.Unmarshal(env.Payload, &p)

	moderation := "clean"
	if containsBlockedTerm(p.Body) {
		moderation = "flagged"
	}
	c.bus.Publish(env.Topic, "COMMENT", map[string]any{
		"sender_id":   c.accountID,
		"room_id":     roomID,
		"body":        p.Body,
		"lang":        p.Lang,
		"moderation":  moderation,
	})
}

// handleModeration implements the host controls from roadmap Phase 6:
// "Host moderation controls (mute, kick) → USER_MUTED/KICKED events" and
// "Enforcement events with rule ID" — every enforcement here carries a
// non-empty rule_id (BT-09 / the architecture's rule_id NOT NULL constraint).
func (c *client) handleModeration(ctx context.Context, env clientEnvelope) {
	roomID, isRoom := topics.IsRoom(env.Topic)
	if !isRoom {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "TOPIC_NOT_FOUND", "topic": env.Topic})
		return
	}
	isHost, err := c.isHost(ctx, roomID, c.accountID)
	if err != nil || !isHost {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
		return
	}
	var p struct {
		TargetID   string `json:"target_id"`
		DurationS  int    `json:"duration_s"`
		RuleID     string `json:"rule_id"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if p.TargetID == "" {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "INVALID_TARGET", "topic": env.Topic})
		return
	}
	if p.RuleID == "" {
		p.RuleID = "CS-00" // generic catch-all rule; a real deployment picks from the moderation_rules catalogue (doc 12 Phase 0)
	}

	if env.Type == "MUTE_USER" {
		c.bus.Publish(env.Topic, "USER_MUTED", map[string]any{
			"target_id": p.TargetID, "duration_s": p.DurationS, "by": "host",
		})
	} else {
		c.bus.Publish(env.Topic, "USER_KICKED", map[string]any{"target_id": p.TargetID})
	}
	c.bus.Publish(env.Topic, "ROOM_MODERATION_NOTICE", map[string]any{
		"message": "A moderation action was taken.",
		"rule_id": p.RuleID,
		"rule_url": "https://lumena.live/rules/" + p.RuleID,
	})
	// Private enforcement notice to the affected user (doc 08 §10: ACCOUNT_STATUS on sys/user topic).
	c.bus.Publish(topics.User(p.TargetID), "ACCOUNT_STATUS", map[string]any{
		"status":  actionStatus(env.Type),
		"rule_id": p.RuleID,
		"rule_url": "https://lumena.live/rules/" + p.RuleID,
		"appealable": true,
	})
}

// authorizedForConv reports whether c.accountID may subscribe to or post on
// conv:{convID}. Private chat is opt-in for the gateway (WithPrivateChat) —
// if it was never wired, every conv: access is denied rather than silently
// allowed.
func (c *client) authorizedForConv(ctx context.Context, convID string) bool {
	if c.isParticipant == nil {
		return false
	}
	ok, err := c.isParticipant(ctx, convID, c.accountID)
	return err == nil && ok
}

func (c *client) authorizedForPV(ctx context.Context, sessionID string) bool {
	if c.isPVParticipant == nil {
		return false
	}
	ok, err := c.isPVParticipant(ctx, sessionID, c.accountID)
	return err == nil && ok
}

// authorizedForUser reports whether c.accountID may subscribe to
// user:{userID} — an account's own private topic (BALANCE_CHANGED,
// DIAMONDS_CHANGED, ACCOUNT_STATUS, PV signaling, translated DMs, ...).
// Unlike authorizedForConv/authorizedForPV this needs no injected lookup:
// the only account ever allowed onto user:{id} is that same authenticated
// connection — no accountID a client sends in a message body is ever
// trusted, only the one bound to the connection at auth time. Without this
// check, any authenticated client could SUBSCRIBE to another account's
// user: topic and observe its private balance/status events.
func (c *client) authorizedForUser(userID string) bool {
	return c.accountID == userID
}

// relayPV passes SDP_OFFER/SDP_ANSWER/ICE_CANDIDATE straight through to
// the other participant (doc 09 §5's diagram) — the payload is opaque to
// the server, same as a real TURN relay never inspecting SRTP.
func (c *client) relayPV(ctx context.Context, env clientEnvelope) {
	sessionID, isPV := topics.IsPV(env.Topic)
	if !isPV || !c.authorizedForPV(ctx, sessionID) {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
		return
	}
	var payload map[string]any
	_ = json.Unmarshal(env.Payload, &payload)
	c.bus.Publish(env.Topic, env.Type, payload)
}

// handlePrivateMessage implements doc 08 §9's MESSAGE on conv:{id}: persist
// via the injected SendPrivateMessageFunc (idempotent on client_msg_id —
// see chatsvc.Service.SendMessage), then fan out the authoritative event
// with server-assigned seq to both participants.
func (c *client) handlePrivateMessage(ctx context.Context, env clientEnvelope) {
	convID, isConv := topics.IsConv(env.Topic)
	if !isConv {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "TOPIC_NOT_FOUND", "topic": env.Topic})
		return
	}
	if c.sendMessage == nil || !c.authorizedForConv(ctx, convID) {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "FORBIDDEN", "topic": env.Topic})
		return
	}
	var p struct {
		Body         string `json:"body"`
		ClientMsgID  string `json:"client_msg_id"`
		AttachmentID string `json:"attachment_id"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if p.ClientMsgID == "" {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "VALIDATION_ERROR", "topic": env.Topic})
		return
	}
	payload, err := c.sendMessage(ctx, c.accountID, convID, p.ClientMsgID, p.Body, p.AttachmentID)
	if err != nil {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "SEND_FAILED", "topic": env.Topic})
		return
	}
	c.bus.Publish(env.Topic, "MESSAGE", payload)
}

// handleReadReceipt implements doc 08 §9's READ on conv:{id}.
func (c *client) handleReadReceipt(ctx context.Context, env clientEnvelope) {
	convID, isConv := topics.IsConv(env.Topic)
	if !isConv || c.markRead == nil || !c.authorizedForConv(ctx, convID) {
		return
	}
	var p struct {
		UpToSeq uint64 `json:"up_to_seq"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if err := c.markRead(ctx, c.accountID, convID, p.UpToSeq); err != nil {
		return
	}
	c.bus.Publish(env.Topic, "READ", map[string]any{"user_id": c.accountID, "up_to_seq": p.UpToSeq})
}

// handleGift implements doc 08 §6: a gift is posted through the injected
// SendGiftFunc (backed by giftsvc + the ledger's idempotent double-entry
// transaction), then fanned out as a room-scoped GIFT_SENT plus two private
// balance events. BALANCE_CHANGED/DIAMONDS_CHANGED go only to topics.User —
// "a room subscriber cannot learn another user's balance from the
// WebSocket" (doc 08 §6).
func (c *client) handleGift(ctx context.Context, env clientEnvelope) {
	roomID, isRoom := topics.IsRoom(env.Topic)
	if !isRoom {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "TOPIC_NOT_FOUND", "topic": env.Topic})
		return
	}
	if c.sendGift == nil {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "GIFTS_UNAVAILABLE", "topic": env.Topic})
		return
	}
	var p struct {
		RecipientID    string `json:"recipient_id"`
		GiftID         string `json:"gift_id"`
		Quantity       int64  `json:"quantity"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	if p.RecipientID == "" || p.GiftID == "" || p.IdempotencyKey == "" {
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "VALIDATION_ERROR", "topic": env.Topic})
		return
	}
	if p.Quantity <= 0 {
		p.Quantity = 1
	}

	result, err := c.sendGift(ctx, c.accountID, p.RecipientID, roomID, p.GiftID, p.Quantity, p.IdempotencyKey)
	if err != nil {
		var shortfall shortfallError
		if errors.As(err, &shortfall) {
			c.sendControl("ERROR", env.Topic, map[string]any{
				"code": "INSUFFICIENT_BALANCE", "topic": env.Topic, "shortfall": shortfall.Shortfall(),
			})
			return
		}
		c.sendControl("ERROR", env.Topic, map[string]string{"code": "GIFT_FAILED", "topic": env.Topic})
		return
	}

	c.bus.Publish(env.Topic, "GIFT_SENT", map[string]any{
		"transaction_id": result["transaction_id"],
		"sender_id":      c.accountID,
		"recipient_id":   p.RecipientID,
		"gift_id":        result["gift_id"],
		"gift_name":      result["gift_name"],
		"gift_asset_url": result["gift_asset_url"],
		"quantity":       result["quantity"],
		"coins":          result["coins_spent"],
	})

	c.bus.Publish(topics.User(c.accountID), "BALANCE_CHANGED", map[string]any{
		"new_balance":    result["new_sender_balance"],
		"delta":          -asInt64(result["coins_spent"]),
		"transaction_id": result["transaction_id"],
		"reason":         "gift_send",
	})
	c.bus.Publish(topics.User(p.RecipientID), "DIAMONDS_CHANGED", map[string]any{
		"new_diamonds":   result["new_recipient_diamonds"],
		"delta":          result["creator_diamonds"],
		"transaction_id": result["transaction_id"],
	})
}

func asInt64(v any) int64 {
	n, _ := v.(int64)
	return n
}

func actionStatus(msgType string) string {
	if msgType == "MUTE_USER" {
		return "muted"
	}
	return "removed"
}

func (c *client) sendControl(typ, topic string, payload any) {
	select {
	case c.send <- bus.Event{Type: typ, Topic: topic, Ts: time.Now().UTC().Format(time.RFC3339Nano), Payload: payload}:
	default:
	}
}

// containsBlockedTerm is a placeholder pre-publish text filter. Real
// moderation (roadmap doc 12 Phase 0/6) runs a classifier service; this
// keeps the wire contract ("moderation":"clean"|"flagged") correct for
// local dev without pretending to have that classifier.
func containsBlockedTerm(body string) bool {
	const blocked = "xxxslur" // obvious placeholder, never a real term list
	return len(body) > 0 && containsFold(body, blocked)
}

func containsFold(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
