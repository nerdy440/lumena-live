# 03 — Screen Inventory

47 screens. Full template expanded for the 8 architecturally load-bearing screens; the remaining 39 are specified in condensed form (§10) with the same field set, to be expanded as their phase is reached. **Condensed ≠ designed-away** — each still requires the full field set before its Definition of Done.

**ID scheme:** `AREA_NNN`. Areas: `AUTH`, `HOME`, `DISC`, `ROOM`, `PROF`, `CHAT`, `PVID`, `MTCH`, `WALL`, `CRTR`, `SETG`, `SUPP`, `MODR`.

**Universal states** (assume on every screen unless overridden): loading = skeleton, never spinner-on-blank; error = inline retry with a specific message, never "Something went wrong"; empty = illustration + one clear action; offline = persistent banner + queued writes.

---

## 1. `ROOM_001` — Live Room (Viewer)

**Purpose:** Watch a live broadcast and participate socially and economically.
**Entry:** Home/Hot/Explore card tap · deep link · notification · profile "Live now" · search result.
**Exit:** Back (→ feed at preserved position, **BT-05**) · profile tap · private chat · stream ended → recap · swipe up/down → next room.

**UI components**
- Video surface (fills, aspect-preserved, tap-to-toggle chrome)
- Host header: avatar, display name, follow/unfollow toggle (**BT-02**), live badge, viewer count
- Viewer avatar rail (top N by contribution, tap → mini profile)
- Chat overlay — **dismissible, resizable, opacity-adjustable (BT-01)**
- Message composer with translation toggle
- Like/reaction burst button
- Gift button → gift drawer sheet
- Gift animation layer (full-screen, non-blocking, skippable)
- Share · Report · More (mute host, hide chat, quality, PiP, block)
- Connection state chip (`LIVE` / `RECONNECTING…` / `PAUSED`)
- Balance pill (tap → top-up sheet)

**Data required:** `room{id,status,title,tags,region,language}`, `host{id,name,avatar,level,follow_state,is_blocked}`, `viewer_count`, `chat_page(cursor)`, `gift_catalogue(region)`, `wallet.balance`, `viewer_perms{can_chat,is_muted}`.

**API:** `GET /v1/rooms/{id}` · `POST /v1/rooms/{id}/join` · `POST /v1/rooms/{id}/leave` · `GET /v1/gifts` · `POST /v1/gifts/send` · `POST /v1/follows` · `DELETE /v1/follows/{id}` · `POST /v1/reports` · `GET /v1/wallet`

**WebSocket events (subscribe `room:{id}`)**
`ROOM_STATE` · `VIEWER_JOINED` · `VIEWER_LEFT` · `VIEWER_COUNT` · `COMMENT` · `COMMENT_TRANSLATED` · `LIKE` · `FOLLOW` · `GIFT_SENT` · `BALANCE_CHANGED` (private) · `USER_MUTED` · `USER_KICKED` · `STREAM_PAUSED` · `STREAM_RECONNECTED` · `STREAM_ENDED` · `MODERATION_NOTICE`

**User actions:** join, leave, comment, like, follow/unfollow, open gift drawer, send gift, top up, share, report, block, mute host, toggle chat, change quality, enter PiP, open profile, start private chat, request private video, swipe to next room.

**Error states:** room ended → recap card w/ "Follow for next time"; room not found → 404 screen; **blocked by host** → explicit, non-ambiguous message; region-restricted; playback failure → retry + quality downgrade; gift failure → *balance never optimistically decremented* (see doc 11 §4); insufficient balance → top-up sheet with the exact shortfall.

**Loading:** blurred host avatar as poster → first frame; chat renders before video if it arrives first.
**Empty:** zero chat → "Say hi to {host}" prompt.
**Animations:** gift effects (original assets, ≤3s, ≤60fps, GPU-composited, **skippable**, auto-degraded on low-end devices); like burst; join banner. All respect `prefers-reduced-motion`.
**Permissions:** none for viewing. Notifications prompt only after first follow — never on entry.
**Monetization:** gift button, balance pill, top-up sheet, private-video CTA, host-level badge.
**Analytics:** `room_view_start`, `room_view_end{duration_ms,exit_reason}`, `comment_sent`, `gift_drawer_open`, `gift_sent{gift_id,coins}`, `follow_toggled`, `topup_initiated`, `chat_visibility_toggled`, `playback_stall{duration_ms}`, `reconnect{attempt,outcome}`.

---

## 2. `ROOM_002` — Live Room (Broadcaster)

**Purpose:** Go live and manage the room.
**Entry:** Home FAB → pre-live setup (`ROOM_003`). **Hard-gated on age assurance (ID-07) and T&S standing.**
**Exit:** End stream → summary (`ROOM_004`).

**Components:** camera preview, title/tag/region editor, camera flip, beauty/filter (original), mic mute, chat panel with **inline moderation actions**, viewer list w/ mute-kick-ban, earnings ticker (diamonds this session), gift feed, health indicator (bitrate/fps/dropped frames/RTT), moderator invite, end-stream.

**Data:** `stream_credentials{ingest_url, stream_key}` (short-lived, single-use), `broadcast_health`, `session_earnings`, `moderation_queue`.
**API:** `POST /v1/streams` · `POST /v1/streams/{id}/start` · `POST /v1/streams/{id}/stop` · `PATCH /v1/streams/{id}` · `POST /v1/rooms/{id}/moderate`
**WS:** all of `ROOM_001` plus `BROADCAST_HEALTH`, `MODERATION_WARNING`, `STREAM_TERMINATED{reason, rule_id}`.

**Error states:** camera/mic denied → explainer + settings deep link; ingest rejected; **network too weak → offer audio-only rather than dropping**; forced termination → **must state the rule violated and the appeal route (BT-09)**.
**Permissions:** camera, microphone (requested at pre-live, with rationale, never at app launch).
**Monetization:** earnings ticker, gift feed, goal widget.
**Analytics:** `broadcast_start`, `broadcast_end{duration,peak_viewers,diamonds}`, `broadcast_health_degraded`, `moderation_action_taken`.

---

## 3. `HOME_001` — Home Feed

**Purpose:** Personalized entry to live content.
**Entry:** app launch (authed) · tab bar.
**Exit:** room · profile · search · notifications.

**Components:** top tabs (For You / Hot / Explore / Following), room card grid (thumbnail, host, title, viewers, region flag, language chip, live pulse), pull-to-refresh, infinite scroll, floating Go Live, notification bell w/ badge.
**Data:** `feed_page{cursor, items[]}`, `unread_counts`.
**API:** `GET /v1/feed?tab=&cursor=&limit=` · `GET /v1/notifications/summary`
**WS:** `FEED_HINT` (lightweight invalidation only — never pushes full items).

**Critical behavior — BT-05:** cursor, scroll offset, and active tab persist across room entry/exit **and process death** (saved-state handle + local cache). Returning from a room must not refetch page 1.

**Error:** feed fetch failure → cached items + retry banner. **Empty:** no live rooms in region → widen-region CTA + upcoming schedule.
**Loading:** shimmer cards at correct aspect ratio (no layout shift).
**Monetization:** none direct. Do not put purchase CTAs in the feed.
**Analytics:** `feed_impression{item_ids,position}`, `feed_card_tap{position,dwell}`, `feed_refresh`, `feed_state_restored{restored:bool}`.

---

## 4. `WALL_001` — Wallet

**Purpose:** Single source of truth for the user's money. This screen is the direct answer to complaints C6 and C7.

**Components:** balance (server value, never cached-optimistic), Top Up, **full transaction ledger** (every row: type, amount, counterparty, status, timestamp, transaction ID, copyable), status filter, pending-order banner with live status, spend-limit settings, "Missing coins?" → prefilled support ticket (**BT-08**).

**Data:** `wallet{balance, currency, updated_at}`, `transactions{cursor, items[]}`, `pending_orders[]`.
**API:** `GET /v1/wallet` · `GET /v1/wallet/transactions` · `GET /v1/orders?status=pending` · `POST /v1/orders` · `POST /v1/orders/{id}/verify`
**WS:** `BALANCE_CHANGED`, `ORDER_STATUS_CHANGED`.

**Order status machine (user-visible, verbatim):**
`CREATED → PENDING_PAYMENT → PAID → CREDITING → CREDITED`, with `FAILED` and `REFUNDED` terminals.
**A user must never see a payment succeed with no explanation of where the coins are.** If an order sits in `PAID` without `CREDITED` for >30s, the UI surfaces "Payment received — crediting your account" plus the order ID, and the reconciler (doc 11 §6) settles it. Silence is a defect.

**Error:** balance fetch failure → last-known **explicitly labelled stale with timestamp**, all spend actions disabled; order verification failure → retry + support path.
**Empty:** no transactions → explainer of how coins work.
**Monetization:** the whole screen.
**Analytics:** `wallet_view`, `topup_initiated{sku}`, `topup_completed{order_id,latency_ms}`, `topup_failed{reason}`, `ledger_scrolled`, `support_from_wallet`.

---

## 5. `WALL_002` — Top-Up Sheet

**Purpose:** Buy coins. **Store IAP only** (Google Play Billing / StoreKit). No QR codes, no bank transfers, no off-platform rails — this is the structural fix for C7.

**Flow:** select SKU → `POST /v1/orders` returns `order_id` + `idempotency_key` → native billing → purchase token → `POST /v1/orders/{id}/verify` → **server verifies with the store** → ledger credit → `BALANCE_CHANGED`.
**Client never credits the wallet.** Client only reports a token; the server decides.

**Error:** billing unavailable; purchase cancelled; **verification pending → order persists and reconciler retries, user is told so with an ID**; already-consumed token → idempotent no-op returning existing order.
**Analytics:** `sku_viewed`, `purchase_started`, `purchase_token_received`, `verify_result{status,latency_ms}`.

---

## 6. `PVID_001` — Private Video Session

**Purpose:** Billed 1-to-1 video. **The highest-risk surface in the product.**

**Preconditions — all must hold, checked server-side, every time:**
1. Both parties age-assured (ID-07)
2. Neither has blocked the other
3. Callee's privacy settings permit calls from caller (SG-09)
4. Caller has sufficient pre-authorized balance
5. Callee is available and has accepted
6. Neither is under an active T&S restriction

**Components:** remote video (full), local PiP (draggable), **always-visible** End / Report / Block, mute audio, mute video, camera flip, **live cost meter (elapsed time + coins spent, updated per billed interval)**, connection quality, low-balance warning at 60s remaining, remaining-time countdown.

**Data:** `session{id, state, peer, started_at, rate_per_minute}`, `billing{authorized, consumed, remaining_seconds}`, `ice_servers`.
**API:** `POST /v1/private-video/requests` · `POST /v1/private-video/requests/{id}/accept|decline` · `POST /v1/private-video/sessions/{id}/end` · `GET /v1/private-video/sessions/{id}`
**WS:** `PV_REQUEST` · `PV_ACCEPTED` · `PV_DECLINED` · `PV_SESSION_STARTED` · `PV_BILLING_TICK` · `PV_LOW_BALANCE` · `PV_PEER_DISCONNECTED` · `PV_RECONNECTING` · `PV_SESSION_ENDED{reason}` · signaling (`SDP_OFFER/ANSWER`, `ICE_CANDIDATE`)

**States:** `REQUESTING → RINGING → AUTHORIZING → CONNECTING → ACTIVE → (RECONNECTING) → ENDING → SETTLED`.
**Billing rule:** meter runs only in `ACTIVE`. **`RECONNECTING` does not bill.** Settlement is computed server-side from server-observed active intervals, never from client-reported duration.

**Error:** peer declined · peer busy · insufficient balance (pre-flight, with exact shortfall) · connection failed after N ICE retries → auto-end + **full refund of the unconsumed hold** · peer disconnected >45s → auto-end + settle at actual consumed · session timeout at configured max.
**Permissions:** camera, mic — requested at accept, not before.
**Safety:** `FLAG_SECURE`; peer notified on screenshot attempt where the OS permits (**deterrent only — documented as not a guarantee**); report is one tap and captures a server-side evidence window; **automated CSAM/nudity classification runs on sampled frames (PV-07, TS-04)** with immediate termination and mandatory reporting on positive match.
**Analytics:** `pv_requested`, `pv_accepted`, `pv_connect_latency_ms`, `pv_session_duration`, `pv_coins_consumed`, `pv_end_reason`, `pv_reconnect_count`, `pv_reported`.

---

## 7. `MTCH_001` — Match

**Purpose:** Server-controlled discovery of a live 1-to-1 partner.
**Entry:** tab bar / Home CTA. **Age-gated.**

**Components:** filter sheet (region, language, interests — **preferences, not guarantees**), Start Matching, searching state with cancel, candidate card (avatar, name, region flag, languages, interests, online), Connect / Skip, cooldown indicator, safety reminder card on first use.
**Data:** `match_preferences`, `match_candidate`, `match_session`, `cooldown_state`.
**API:** `POST /v1/match/requests` · `GET /v1/match/requests/{id}` · `POST /v1/match/decisions` · `DELETE /v1/match/requests/{id}`
**WS:** `MATCH_SEARCHING` · `MATCH_CANDIDATE` · `MATCH_TIMEOUT` · `MATCH_SESSION_CREATED` · `MATCH_CANCELLED`

**Server guarantees (never client-side):** blocked users are never surfaced in either direction; users under restriction are excluded; repeat-report offenders are excluded from the pool; rate limits prevent queue farming; **sensitive attributes are used for filtering but never returned to the client** (doc 06 §7).
**Error:** no candidates → widen filters CTA; timeout → retry; rate-limited → cooldown w/ time remaining.
**Analytics:** `match_started`, `match_candidate_shown`, `match_decision{connect|skip}`, `match_timeout`, `match_to_session_rate`.

---

## 8. `PROF_001` — User Profile

**Purpose:** Identity, social actions, relationship management.

**Components:** header (avatar, name, bio, level, region, languages), counts (followers / following / received-gift score), **Follow / Following toggle — tapping "Following" unfollows directly (BT-02)**, Message, Private Video CTA (if permitted), Gift, live indicator, content tabs, overflow: Block · Report · Mute · Share · Copy ID.
**Data:** `user`, `relationship{following, followed_by, blocked, blocked_by, muted}`, `live_status`, `privacy_permissions`.
**API:** `GET /v1/users/{id}` · `GET /v1/users/{id}/relationship` · `POST|DELETE /v1/follows` · `POST|DELETE /v1/blocks` · `POST /v1/reports`

**Consistency requirement (BT-03):** follow/unfollow returns the authoritative new relationship object; the client renders from the response, not from an optimistic guess. The Following list is read-your-own-writes consistent — implementation in doc 06 §4.
**Error:** user not found · suspended (neutral message) · **blocked-by-them → honest but non-inflammatory: "This profile isn't available."**
**Analytics:** `profile_view{source}`, `follow_toggled{new_state}`, `block_toggled`, `report_opened`, `pv_cta_tapped`.

---

## 9. Cross-cutting requirements for every screen

1. Back navigation restores prior state — no exceptions (**BT-05**).
2. Every destructive action is confirmable and, where possible, reversible.
3. Every error names a cause and offers an action.
4. Every screen with money shows server-authoritative values or is explicitly labelled stale.
5. Report is reachable in ≤2 taps from any surface with user content.
6. Accessibility: labelled controls, 4.5:1 contrast, 44pt targets, dynamic type, reduced-motion honored.
7. No permission is requested before the action requiring it.
8. Every enforcement-related message carries a rule reference and appeal path (**BT-09**).

---

## 10. Condensed inventory — remaining 39

| ID | Name | Entry | Key actions | Notable states |
|---|---|---|---|---|
| `AUTH_001` | Splash | launch | — | token refresh, forced-update, region check |
| `AUTH_002` | Welcome / value prop | splash (unauth) | sign in, continue as guest | — |
| `AUTH_003` | Phone entry | welcome | country select, submit | invalid, rate-limited, region blocked |
| `AUTH_004` | OTP verify | AUTH_003 | enter, resend | expired, wrong code, lockout |
| `AUTH_005` | Email/password | welcome | sign in, forgot | invalid creds, locked |
| `AUTH_006` | Password reset | AUTH_005 | request, set new | token expired |
| `AUTH_007` | OAuth handoff | welcome | Google/Apple | cancelled, email conflict |
| `AUTH_008` | **Age declaration** | post-signup | DOB entry | under-minimum → **hard stop** |
| `AUTH_009` | **Age assurance** | before broadcast/PV | doc/estimation flow | pending, failed, appeal |
| `AUTH_010` | Onboarding: profile | post-auth | name, avatar, languages | avatar rejected by moderation |
| `AUTH_011` | Onboarding: interests | AUTH_010 | multi-select | — |
| `AUTH_012` | Onboarding: follow suggestions | AUTH_011 | follow, skip | empty |
| `HOME_002` | Hot | tab | scroll, enter | — |
| `DISC_001` | Explore | tab | category/region/language browse | empty per filter |
| `DISC_002` | Category detail | DISC_001 | scroll, enter | empty |
| `DISC_003` | Search | icon | query, filter | no results, history, suggestions |
| `DISC_004` | Following feed | tab | scroll | **empty → "you follow no one yet"** |
| `ROOM_003` | Pre-live setup | Go Live FAB | title, tags, cover, region, permissions | **blocked if not age-assured** |
| `ROOM_004` | Stream summary | end broadcast | share, view earnings | — |
| `ROOM_005` | Stream ended (viewer) | STREAM_ENDED | follow, next room | — |
| `ROOM_006` | Gift drawer | gift button | select, quantity, send, top up | insufficient balance |
| `ROOM_007` | Viewer list | count tap | profile, moderate | — |
| `ROOM_008` | Mini profile sheet | avatar tap | follow, message, report | — |
| `CHAT_001` | Conversation list | tab | open, delete, mute | empty, requests folder |
| `CHAT_002` | Conversation | CHAT_001 | send, attach, delete, block, report | blocked, rate-limited, not-mutual restriction |
| `CHAT_003` | Message requests | CHAT_001 | accept, decline, report | empty |
| `CHAT_004` | Attachment viewer | CHAT_002 | save, report | load failure |
| `PVID_002` | Incoming call | push/WS | accept, decline, block | timeout |
| `PVID_003` | Call ended summary | PVID_001 | rate, report, tip | — |
| `PVID_004` | Call history | settings/profile | view, report | empty |
| `MTCH_002` | Match filters | MTCH_001 | set prefs | unavailable-in-region filters |
| `MTCH_003` | Match history | MTCH_001 | view, report | empty |
| `PROF_002` | Edit profile | PROF_001 | edit fields | validation, moderation rejection |
| `PROF_003` | Followers list | PROF_001 | follow, remove | empty |
| `PROF_004` | Following list | PROF_001 | **unfollow inline (BT-02)** | empty |
| `PROF_005` | Blocked users | settings | unblock | empty |
| `WALL_003` | Transaction detail | WALL_001 | copy ID, dispute | — |
| `CRTR_001` | Creator dashboard | profile | earnings, analytics, payout | not-eligible state |
| `CRTR_002` | Earnings ledger | CRTR_001 | filter, export | empty |
| `CRTR_003` | Payout request | CRTR_001 | request | **KYC-blocked, hold-period, threshold** |
| `SETG_001` | Settings root | profile | navigate | — |
| `SETG_002` | Privacy settings | SETG_001 | who can DM/call, presence | — |
| `SETG_003` | Notification settings | SETG_001 | per-category toggles | — |
| `SETG_004` | Spend limits | SETG_001 | set caps, cooling-off | — |
| `SETG_005` | Account & data | SETG_001 | export, **delete account** | deletion grace period |
| `SUPP_001` | Help center | settings | browse, search | — |
| `SUPP_002` | **Ticket list** | SUPP_001 | view status, reply | empty |
| `SUPP_003` | New ticket | SUPP_001 | submit, attach | — |
| `MODR_001` | Report flow | any report entry | reason, detail, evidence, submit | — |
| `MODR_002` | **My enforcement notices** | settings/push | view reason + rule, **appeal** | empty |
| `MODR_003` | Appeal submission | MODR_002 | statement, submit | pending, decided |
