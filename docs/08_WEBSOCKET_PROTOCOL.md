# 08 — WebSocket Protocol

Single persistent connection per client. Topics are subscribed over the connection — not separate sockets per feature.

---

## 1. Connection

```
wss://rt.lumena.live/ws?token=<short_lived_ws_token>&device=<device_id>
```

`ws_token` is exchanged for a short-lived (60s) single-use WebSocket token via `GET /auth/ws-token` immediately before connecting. It is scoped to the account and device, is never the same as the access token, and is rotated on reconnect.

**Why a separate token?** The access token goes in an HTTP header. WebSocket URLs land in server logs, browser history, and Referer headers. Keeping a short-lived single-use token in the URL and the long-lived access token off the URL removes the most common token-leak vector for WebSocket APIs.

Heartbeat: client sends `PING` every 25s; server replies `PONG` within 5s or the connection is considered dead. Server sends `PING` every 30s; client replies or is disconnected.

---

## 2. Envelope

Every message in both directions uses this envelope:

```json
{
  "msg_id":   "01J8XR...",
  "type":     "COMMENT",
  "topic":    "room:01J8AA...",
  "seq":      1042,
  "ts":       "2026-09-08T10:30:00.123Z",
  "payload":  { ... }
}
```

| Field | Notes |
|---|---|
| `msg_id` | ULID. Client uses for deduplication |
| `type` | Stable enum — see §3–7 |
| `topic` | `room:{id}` · `user:{id}` · `conv:{id}` · `pv:{id}` · `sys` |
| `seq` | Monotonic per topic, server-assigned. Gap → request backfill |
| `ts` | Server timestamp. Client displays relative ("2s ago"); never trusts its own clock for ordering |
| `payload` | Type-specific object |

Client messages follow the same envelope but use `client_seq` (client-assigned, for ack tracking) instead of `seq`.

---

## 3. Subscription management

```json
// Subscribe
{ "msg_id":"...", "type":"SUBSCRIBE", "topic":"room:01J8AA...", "payload":{} }
// Unsubscribe
{ "msg_id":"...", "type":"UNSUBSCRIBE", "topic":"room:01J8AA...", "payload":{} }
// Backfill — request missed events after a gap
{ "msg_id":"...", "type":"BACKFILL", "topic":"room:01J8AA...",
  "payload":{ "from_seq": 1039 } }
```

Server replies:
```json
{ "type":"SUBSCRIBED", "topic":"room:01J8AA...", "payload":{ "last_seq":1042 } }
{ "type":"BACKFILL_RESULT", "topic":"...", "payload":{ "events":[ ... ] } }
{ "type":"ERROR", "payload":{ "code":"TOPIC_NOT_FOUND", "topic":"..." } }
```

Automatic subscriptions on connect (no client action needed): `user:{self_id}` for private events, `sys` for global announcements.

---

## 4. Video events — topic `room:{id}`

```json
{ "type":"STREAM_STARTED",    "payload":{ "room_id":"...", "playback_url":"...", "started_at":"..." } }
{ "type":"STREAM_PAUSED",     "payload":{ "room_id":"...", "reason":"host_action|network" } }
{ "type":"STREAM_RECONNECTED","payload":{ "room_id":"...", "playback_url":"..." } }
{ "type":"STREAM_ENDED",      "payload":{ "room_id":"...", "reason":"host_ended|terminated|error",
                                          "rule_id":"CS-03", "duration_s":3600 } }
```

`STREAM_ENDED` carries `rule_id` when the reason is `terminated` — so viewers see "This stream ended due to a community guidelines violation" with a link to the rule, not a blank screen. That is the transparency contract.

---

## 5. Social events — topic `room:{id}`

```json
{ "type":"VIEWER_JOINED","payload":{ "user_id":"...","display_name":"...","avatar_url":"...","level":12 } }
{ "type":"VIEWER_LEFT",  "payload":{ "user_id":"..." } }
{ "type":"VIEWER_COUNT", "payload":{ "count":4200, "approx":true } }

{ "type":"COMMENT", "payload":{
    "msg_id":"...", "sender_id":"...", "display_name":"...", "avatar_url":"...",
    "level":12, "body":"hello!", "lang":"en", "translated_body":null,
    "seq":1042, "moderation":"clean" } }
{ "type":"COMMENT_TRANSLATED", "payload":{
    "ref_msg_id":"...", "translated_body":"...", "target_lang":"ar", "confidence":0.97 } }

{ "type":"LIKE",   "payload":{ "user_id":"...", "count":5 } }
{ "type":"FOLLOW", "payload":{ "user_id":"...", "display_name":"..." } }
```

`COMMENT_TRANSLATED` is a separate, subsequent event so the original comment renders immediately and the translation arrives asynchronously without blocking the chat pipeline. The UI patches the existing message — original is never replaced.

---

## 6. Economic events

Room-scoped (topic `room:{id}`) — visible to all room subscribers:
```json
{ "type":"GIFT_SENT", "payload":{
    "transaction_id":"...", "sender_id":"...", "sender_name":"...",
    "recipient_id":"...", "gift_id":"g_aurora", "gift_name":"Aurora",
    "gift_asset_url":"...", "quantity":1, "coins":500 } }
```

Private (topic `user:{self_id}`) — visible only to the account holder:
```json
{ "type":"BALANCE_CHANGED", "payload":{
    "new_balance":9500, "delta":-500, "transaction_id":"...",
    "reason":"gift_send", "updated_at":"..." } }
{ "type":"DIAMONDS_CHANGED","payload":{ "new_diamonds":4700, "delta":425,
                                        "transaction_id":"...", "updated_at":"..." } }
{ "type":"ORDER_STATUS_CHANGED","payload":{
    "order_id":"...", "status":"credited", "coins_credited":10000,
    "new_balance":19500 } }
```

`BALANCE_CHANGED` is never sent to a room topic — the balance of a private wallet is private. A room subscriber cannot learn another user's balance from the WebSocket.

---

## 7. Moderation events — topic `room:{id}`

```json
{ "type":"USER_MUTED",  "payload":{ "target_id":"...", "duration_s":300, "by":"host|moderator|system" } }
{ "type":"USER_KICKED", "payload":{ "target_id":"..." } }
{ "type":"ROOM_MODERATION_NOTICE","payload":{ "message":"...", "rule_id":"...", "rule_url":"..." } }
```

When `target_id == self_id`, the client surfaces the reason and appeal path. For other targets, only anonymous outcome is shown ("A user was removed"). The target's identity is not broadcast.

---

## 8. Private video signaling — topic `pv:{session_id}`

```json
{ "type":"PV_REQUEST",   "payload":{ "session_id":"...","caller_id":"...","caller_name":"...",
                                     "caller_avatar":"...","rate_coins_per_min":10,
                                     "expires_at":"..." } }
{ "type":"PV_ACCEPTED",  "payload":{ "session_id":"..." } }
{ "type":"PV_DECLINED",  "payload":{ "session_id":"...", "reason":"declined|busy|unavailable" } }
{ "type":"PV_SESSION_STARTED", "payload":{ "session_id":"..." } }
{ "type":"PV_BILLING_TICK",    "payload":{ "elapsed_s":60, "consumed_coins":10, "remaining_coins":490 } }
{ "type":"PV_LOW_BALANCE",     "payload":{ "remaining_coins":30, "remaining_s":180 } }
{ "type":"PV_PEER_DISCONNECTED","payload":{ "session_id":"...","reconnecting":true } }
{ "type":"PV_RECONNECTING",    "payload":{ "session_id":"...", "billing_paused":true } }
{ "type":"PV_SESSION_ENDED",   "payload":{ "session_id":"...","reason":"...","consumed_coins":250,
                                           "duration_s":1500 } }

// WebRTC signaling — passed through the server, server-opaque
{ "type":"SDP_OFFER",      "payload":{ "session_id":"...", "sdp":"..." } }
{ "type":"SDP_ANSWER",     "payload":{ "session_id":"...", "sdp":"..." } }
{ "type":"ICE_CANDIDATE",  "payload":{ "session_id":"...", "candidate":"...",
                                       "sdpMid":"...", "sdpMLineIndex":0 } }
```

`PV_BILLING_TICK` events are server-generated every 60s from server-observed intervals. The client renders the meter from these; it never computes billing locally.

---

## 9. Private chat — topic `conv:{conversation_id}`

```json
{ "type":"MESSAGE",     "payload":{ "msg_id":"...","sender_id":"...","body":"...",
                                    "seq":42,"delivered_at":"..." } }
{ "type":"TYPING",      "payload":{ "user_id":"..." } }
{ "type":"READ",        "payload":{ "user_id":"...","up_to_seq":42 } }
{ "type":"DELIVERED",   "payload":{ "msg_id":"...","seq":42 } }
{ "type":"MSG_DELETED", "payload":{ "msg_id":"...","scope":"me|everyone" } }
```

---

## 10. System topic `sys`

```json
{ "type":"FORCE_RELOAD",   "payload":{ "reason":"config_change" } }
{ "type":"MAINTENANCE",    "payload":{ "starts_at":"...","duration_min":30,"message":"..." } }
{ "type":"ACCOUNT_STATUS", "payload":{ "status":"restricted","case_id":"ENF-4K7Q",
                                       "rule_id":"CS-03","rule_url":"...","appealable":true } }
{ "type":"TOKEN_EXPIRING", "payload":{ "expires_in_s":120 } }
```

`ACCOUNT_STATUS` delivers enforcement notices in real time so a user in a live room knows immediately why their chat is disabled — rather than discovering it when a message silently fails.

---

## 11. Reconnect protocol

On disconnect, clients use exponential backoff with jitter:

```
attempt 1: 1s ± 0.5s
attempt 2: 2s ± 1s
attempt 3: 4s ± 2s
attempt n: min(60s, 2^n * 0.5s) ± jitter
```

After reconnect:
1. Client sends `SUBSCRIBE` for all previously-held topics with its last known `seq` per topic.
2. Server compares to current sequence and returns `BACKFILL_RESULT` for any gap ≤ 500 events.
3. Gap > 500: server sends `CATCHUP_REQUIRED`, client re-fetches via REST (this is the "left a live and lost my place" fix — reconnect within the window is seamless; only a very large gap triggers a full reload).
4. Client applies `msg_id` deduplication from its local ring buffer before rendering backfilled events.

A private video session persists on the server through reconnect. The session stays in `reconnecting` state (billing paused) for up to 45s, then settles if the client hasn't returned.

---

## 12. Gateway sharding

Connections are sharded by `account_id % N` across gateway nodes. Room topics are subscribed across nodes by a Kafka-backed fan-out bus. A gift event in room `01J8AA` is:
1. Written to ledger (single authoritative write)
2. Emitted to Kafka `room-events` partition `hash(room_id) % 32`
3. Consumed by every gateway node that has subscribers for that room
4. Written to those connections

No direct gateway-to-gateway communication — the bus is the communication layer.

---

## 13. Client-side deduplication

Each client maintains a ring buffer of the last 500 `msg_id` values received per topic. Any event whose `msg_id` is in the buffer is silently dropped before rendering. This handles:
- At-least-once delivery from the bus
- Backfill overlap on reconnect
- Duplicate events from transient gateway splits

The ring buffer is in-memory only and resets on process restart.
