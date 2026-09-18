# 07 — API Specification

Base: `https://api.lumena.live/v1`. JSON only. TLS 1.3. All timestamps RFC3339 UTC.

---

## 1. Conventions

**Auth:** `Authorization: Bearer <access_token>` (JWT, 15 min). Refresh via rotating refresh token bound to `device_id`; reuse of a rotated refresh token revokes the whole family.

**Idempotency:** `Idempotency-Key: <uuid>` header. **Required** on `POST /gifts/send`, `POST /orders`, `POST /orders/{id}/verify`, `POST /private-video/requests`, `POST /payouts`. Optional elsewhere. Retention 24h. Replay returns the original response body with `Idempotent-Replay: true`.

**Pagination:** opaque cursors. `?cursor=&limit=` → `{ items, next_cursor }`. No offset pagination on feeds — offsets skip and duplicate under live insertion.

**Errors** — always this shape, always actionable:
```json
{ "error": { "code": "INSUFFICIENT_BALANCE",
             "message": "You need 200 more coins to send this gift.",
             "details": { "required": 500, "available": 300, "shortfall": 200 },
             "trace_id": "01J8...", "retryable": false } }
```
`code` is a stable enum for clients. `message` is localized and human. `trace_id` appears in support tickets. **No endpoint returns a bare 500 with "Something went wrong."**

**Rate limits:** `X-RateLimit-Limit / -Remaining / -Reset`; 429 with `Retry-After`.

**Versioning:** URL major version. Breaking changes = new major, 12-month overlap.

---

## 2. Auth

| Method | Path | Notes |
|---|---|---|
| POST | `/auth/phone/start` | `{phone_e164}` → `{challenge_id, expires_at}`. Rate-limited per phone + per IP |
| POST | `/auth/phone/verify` | `{challenge_id, code}` → tokens. 5 attempts then lockout |
| POST | `/auth/email/register` | `{email, password}` — password policy enforced server-side |
| POST | `/auth/email/login` | Constant-time; identical response for unknown user and bad password |
| POST | `/auth/oauth/{provider}` | `{id_token}` |
| POST | `/auth/refresh` | Rotating; reuse detection |
| POST | `/auth/logout` | Revokes current session |
| GET | `/auth/sessions` | List devices |
| DELETE | `/auth/sessions/{id}` | Revoke one |
| POST | `/auth/recover/start` | **BT-10** self-service |
| POST | `/auth/recover/complete` | |

`POST /auth/age/declare` → `{dob}`. Under minimum ⇒ `403 UNDER_AGE_MINIMUM`, account marked, **not** silently allowed with a flag.
`POST /auth/age/assure` → starts vendor flow, returns `{assurance_url, reference}`. `GET /auth/age/status` → `{status, method, assured_at}`.

---

## 3. Users & social graph

| Method | Path | Notes |
|---|---|---|
| GET | `/me` | Full self object incl. `age_status`, `privacy`, `restrictions` |
| PATCH | `/me` | Profile edit; avatar enters moderation queue |
| GET | `/users/{id}` | 404 for suspended/blocked-by — indistinguishable by design |
| GET | `/users/{id}/relationship` | `{following, followed_by, blocked, blocked_by, muted, can_dm, can_call}` |
| POST | `/follows` | `{followee_id}` → **returns full relationship object** |
| DELETE | `/follows/{followee_id}` | **First-class unfollow (BT-02).** Returns relationship object |
| GET | `/users/{id}/followers` | |
| GET | `/users/{id}/following` | Owner's own list served from primary (**BT-03**) |
| POST | `/blocks` | `{blocked_id}`. Does **not** touch follow rows in either direction |
| DELETE | `/blocks/{id}` | |
| POST | `/mutes` / DELETE `/mutes/{id}` | |
| GET | `/me/privacy` / PATCH `/me/privacy` | |

Follow/unfollow return the authoritative relationship so the client renders from truth rather than from an optimistic guess. Block never writes a follow row — the two operations do not know about each other.

```
POST /follows            → 200 {"relationship": {"following": true,  "followed_by": false, ...}}
DELETE /follows/{id}     → 200 {"relationship": {"following": false, "followed_by": false, ...}}
```
Both are idempotent: following twice returns `following: true` and does not error.

---

## 4. Discovery

| Method | Path | Notes |
|---|---|---|
| GET | `/feed?tab=for_you\|hot\|explore\|following&cursor=&limit=` | Cursor encodes ranking snapshot so pagination is stable |
| GET | `/explore/categories` | |
| GET | `/search?q=&type=user\|room\|tag&cursor=` | |
| GET | `/search/suggestions?q=` | |

---

## 5. Rooms

| Method | Path | Notes |
|---|---|---|
| GET | `/rooms/{id}` | Room + host + viewer count + own permissions |
| POST | `/rooms/{id}/join` | Returns `{ws_topic, playback_url, chat_cursor}` |
| POST | `/rooms/{id}/leave` | |
| GET | `/rooms/{id}/messages?cursor=` | Backfill |
| POST | `/rooms/{id}/messages` | `{body, client_msg_id}`. Pre-publish moderation |
| POST | `/rooms/{id}/likes` | `{count}` — batched, server-capped |
| POST | `/rooms/{id}/moderate` | `{target_id, action, duration}`. Host/mod only |
| GET | `/rooms/{id}/viewers?cursor=` | |
| POST | `/streams` | Create broadcast. **403 `AGE_ASSURANCE_REQUIRED` if not assured** |
| POST | `/streams/{id}/start` | Returns short-lived single-use ingest credentials |
| POST | `/streams/{id}/stop` | |
| PATCH | `/streams/{id}` | Title/tags/cover |

---

## 6. Wallet & orders

| Method | Path | Notes |
|---|---|---|
| GET | `/wallet` | `{balance, currency, updated_at}` — server truth |
| GET | `/wallet/transactions?cursor=&type=` | **BT-07** full ledger |
| GET | `/wallet/transactions/{id}` | |
| GET | `/products` | Region-priced SKUs from store config |
| POST | `/orders` | **Idempotent.** `{sku}` → `{order_id, status:"created"}` |
| POST | `/orders/{id}/verify` | **Idempotent.** `{purchase_token}` → server verifies with store |
| GET | `/orders?status=` | Pending order surfacing |
| GET | `/orders/{id}` | Full status machine |
| POST | `/orders/{id}/dispute` | Creates support ticket, returns case ID |
| GET/PATCH | `/me/spend-limits` | Self-set caps + cooling-off |

**Verify is the only path that credits coins.** The client cannot credit, cannot assert a balance, and cannot skip verification. If verification is pending, the response is:
```json
{ "order_id":"...", "status":"paid", "message":"Payment received — crediting your account.",
  "estimated_completion":"2026-09-08T10:31:00Z", "support_reference":"ORD-8F2K" }
```
Never silence. That single behavior is the difference between our wallet and the one users publicly called fraudulent.

---

## 7. Gifts

| Method | Path | Notes |
|---|---|---|
| GET | `/gifts?region=` | Catalogue with coin prices and asset URLs |
| POST | `/gifts/send` | **Idempotency-Key required** |
| GET | `/gifts/received?cursor=` | Creator view |

```
POST /gifts/send
Idempotency-Key: 01J8XR...
{ "gift_id":"g_aurora", "room_id":"...", "recipient_id":"...", "quantity":1 }

200 { "transaction_id":"...", "gift":{...}, "quantity":1,
      "coins_spent":500, "new_balance":9500, "created_at":"..." }

402 INSUFFICIENT_BALANCE { "required":500, "available":300, "shortfall":200 }
403 RECIPIENT_BLOCKED | ROOM_NOT_LIVE | USER_MUTED_IN_ROOM
409 GIFT_UNAVAILABLE_IN_REGION
```
`new_balance` is returned so the client never has to compute it. Retry with the same key returns the identical body with `Idempotent-Replay: true` — one gift, one charge, regardless of how many times a flaky connection retries.

---

## 8. Private chat

| Method | Path | Notes |
|---|---|---|
| GET | `/conversations?cursor=&folder=inbox\|requests` | |
| GET | `/conversations/{id}/messages?cursor=` | |
| POST | `/conversations/{id}/messages` | `{body, client_msg_id, attachment_id}` — idempotent on `client_msg_id` |
| POST | `/conversations/{id}/read` | `{up_to_seq}` |
| DELETE | `/messages/{id}?scope=me\|everyone` | Tombstone |
| POST | `/conversations/{id}/accept` / `/decline` | Request folder |
| POST | `/attachments` | Presigned upload; scanned before deliverable |

---

## 9. Private video

| Method | Path | Notes |
|---|---|---|
| POST | `/private-video/requests` | **Idempotent.** Runs full gate chain server-side |
| GET | `/private-video/requests/{id}` | |
| POST | `/private-video/requests/{id}/accept` | Places billing hold |
| POST | `/private-video/requests/{id}/decline` | |
| DELETE | `/private-video/requests/{id}` | Caller cancels |
| GET | `/private-video/sessions/{id}` | State + billing |
| POST | `/private-video/sessions/{id}/end` | Triggers settlement |
| GET | `/private-video/sessions/{id}/ice` | Short-lived TURN credentials |
| GET | `/private-video/history?cursor=` | |

```
POST /private-video/requests → 403 variants (all render identically in UI):
  AGE_ASSURANCE_REQUIRED   (self — actionable, so it IS specific)
  RECIPIENT_UNAVAILABLE    (covers: blocked, blocked_by, privacy, restricted, not-assured)
402 INSUFFICIENT_BALANCE   { "required_minimum":300, "available":50, "shortfall":250 }
```
The deliberate collapse of five distinct callee conditions into one `RECIPIENT_UNAVAILABLE` is a safety decision, documented in doc 04 §3.

---

## 10. Match

| Method | Path | Notes |
|---|---|---|
| POST | `/match/requests` | `{preferences}`. Age-gated, opt-in only |
| GET | `/match/requests/{id}` | |
| DELETE | `/match/requests/{id}` | Cancel |
| POST | `/match/decisions` | `{request_id, candidate_id, decision}` |
| GET/PATCH | `/me/match-preferences` | |
| GET | `/match/history?cursor=` | |

Candidate payload is minimal and deliberately excludes score internals:
```json
{ "candidate_id":"...", "display_name":"...", "avatar_url":"...",
  "region_code":"PK", "languages":["ur","en"], "interests":["music"], "is_online":true }
```
No score, no `score_reasons`, no age, no gender field, no distance. A serializer test asserts this exact key set.

---

## 11. Moderation & support

| Method | Path | Notes |
|---|---|---|
| POST | `/reports` | Returns `case_id` immediately |
| GET | `/me/enforcements` | **BT-09** — rule, evidence window, duration, appeal eligibility |
| GET | `/me/enforcements/{id}` | |
| POST | `/me/enforcements/{id}/appeal` | Returns `case_id` |
| GET | `/policies/rules` | Public rule catalogue with stable IDs |
| POST | `/support/tickets` | **BT-08** — returns ticket ID |
| GET | `/support/tickets?cursor=` | With status + SLA |
| POST | `/support/tickets/{id}/messages` | |

```
GET /me/enforcements → 200
{ "items":[{ "id":"...", "case_id":"ENF-4K7Q",
             "rule":{"id":"CS-03","title":"Sexual content in public rooms",
                     "url":"https://lumena.live/policy#CS-03"},
             "action":"restrict_broadcast", "duration_hours":72,
             "evidence_window":{"from":"...","to":"..."},
             "decided_by":"human", "appealable":true,
             "expires_at":"..." }] }
```

---

## 12. Creator

| Method | Path | Notes |
|---|---|---|
| GET | `/creator/summary` | Diamonds, followers, sessions |
| GET | `/creator/earnings?cursor=&from=&to=` | Ledger view |
| GET | `/creator/analytics?range=` | |
| POST | `/payouts` | **Idempotent. 403 if KYC incomplete or hold period active** |
| GET | `/payouts?cursor=` | |
| GET/POST | `/creator/kyc` | Vendor handoff |

---

## 13. Platform

| Method | Path |
|---|---|
| GET | `/notifications?cursor=` · POST `/notifications/read` · GET `/notifications/summary` |
| GET/PATCH | `/me/notification-settings` |
| POST | `/devices` (push token registration) · DELETE `/devices/{id}` |
| GET | `/config` (feature flags, min version, region availability) |
| POST | `/me/delete` (grace period) · POST `/me/delete/cancel` · GET `/me/export` |

---

## 14. Status codes

| Code | Use |
|---|---|
| 200 / 201 / 204 | Success |
| 400 `VALIDATION_ERROR` | Malformed |
| 401 `UNAUTHENTICATED` | Missing/expired token |
| 403 | `FORBIDDEN`, `AGE_ASSURANCE_REQUIRED`, `ACCOUNT_RESTRICTED`, `RECIPIENT_UNAVAILABLE`, `REGION_UNAVAILABLE` |
| 404 `NOT_FOUND` | Also used for "exists but you may not see it" |
| 409 | `CONFLICT`, `ALREADY_EXISTS`, `STATE_INVALID` |
| 402 `INSUFFICIENT_BALANCE` | Always includes exact shortfall |
| 422 `UNPROCESSABLE` | Semantically invalid |
| 429 `RATE_LIMITED` | With `Retry-After` |
| 451 `LEGALLY_UNAVAILABLE` | Jurisdictional block |
| 503 `SERVICE_DEGRADED` | Names which subsystem, so the client can degrade correctly |
