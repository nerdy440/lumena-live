# 05 — System Architecture

## 1. Principles

1. **Server-authoritative state.** The client renders; it does not decide. Balance, follow state, session validity, and eligibility are server facts.
2. **No user-visible acknowledgement before durable commit.** This is the root cause of complaints C4/C5/C6 in the analyzed competitor, and the single most important rule here.
3. **Money is double-entry and append-only.** Balances are derived, never edited.
4. **Idempotency everywhere a retry is possible.** Every mutating endpoint accepts an idempotency key; gifting and purchase *require* one.
5. **Modular monolith first, services where the axis is real.** Splitting into 15 microservices on day one buys distributed-transaction pain, not scale. We split only where the scaling or failure axis genuinely differs: real-time gateway, media, moderation, and ledger.
6. **Safety is in the data path, not a batch job.** A nudity classifier that runs 10 minutes later has not protected anyone.
7. **Degrade, don't fail.** Translation down → original text. Recommendations down → recency fallback. Gift animations down → static. Ledger down → **reject writes** (the one place we fail closed).

---

## 2. Component map

```
                        ┌──────────────────────────────────┐
   Android / iOS ───┐   │            EDGE                  │
   Web (PWA)     ───┼──►│  CDN · WAF · rate limit · TLS    │
   Broadcaster SDK ─┘   └───────────┬──────────────────────┘
                                    │
                    ┌───────────────┴──────────────────┐
                    │                                  │
            ┌───────▼────────┐              ┌──────────▼─────────┐
            │  API GATEWAY   │              │  REALTIME GATEWAY  │
            │  REST/HTTP     │              │  WebSocket         │
            │  authN/authZ   │              │  sticky, sharded   │
            └───────┬────────┘              └──────────┬─────────┘
                    │                                  │
        ┌───────────┴──────────────────────────────────┴────────────┐
        │                  CORE APPLICATION (modular monolith)      │
        │                                                            │
        │  identity │ profile │ social-graph │ feed │ rooms │ chat   │
        │  match    │ notify  │ support      │ translation-facade    │
        └───────────┬──────────────────┬───────────────┬────────────┘
                    │                  │               │
        ┌───────────▼───────┐  ┌───────▼──────┐  ┌─────▼──────────┐
        │  LEDGER SERVICE   │  │  MODERATION  │  │  MEDIA CONTROL │
        │  (isolated)       │  │   SERVICE    │  │    SERVICE     │
        │  wallet·gifts     │  │ text·image   │  │ ingest·SFU     │
        │  orders·payouts   │  │ video·CSAM   │  │ CDN·recording  │
        └───────────┬───────┘  └───────┬──────┘  └─────┬──────────┘
                    │                  │               │
        ┌───────────▼──────────────────▼───────────────▼──────────┐
        │  DATA:  Postgres (primary) · Redis · Kafka · S3 · Search │
        └──────────────────────────────────────────────────────────┘
                                    │
        ┌───────────────────────────▼──────────────────────────────┐
        │  ASYNC WORKERS                                            │
        │  reconciler · fraud scoring · feed ranking · translation  │
        │  notification fanout · analytics ETL · payout batch       │
        └───────────────────────────────────────────────────────────┘
```

## 3. Why these four are separated out

| Service | Separate because |
|---|---|
| **Realtime gateway** | Connection-count scaling is orthogonal to request-rate scaling. 500k idle sockets and 5k RPS are different machines. |
| **Ledger** | Different consistency requirements (strict serializable), different compliance surface (audit, retention), different blast radius. Must be independently deployable and independently freezable. |
| **Moderation** | Different latency profile, GPU-bound, third-party model dependencies, and must be able to act on any surface without a circular dependency on the core app. |
| **Media control** | Different failure domain and vendor coupling; must fail without taking chat and social down with it. |

Everything else — identity, profile, graph, feed, chat, match, notifications, support — lives in one deployable with clean internal module boundaries. Extraction later is a refactor, not a rewrite, because the module boundaries are enforced from the start (no cross-module DB access; modules talk through interfaces).

## 4. Technology choices

| Layer | Choice | Reasoning |
|---|---|---|
| Core app | **Go** (or Kotlin/JVM) | Concurrency profile fits fanout; strong typing for a money system |
| Realtime gateway | **Go**, epoll-based WS | Hundreds of thousands of sockets per node |
| Ledger | **Go + Postgres**, serializable isolation | Correctness over throughput; this is the one place we accept slower |
| Primary DB | **PostgreSQL 16** | Transactions, `SERIALIZABLE`, partial indexes, partitioning, JSONB. Sharding deferred until measured |
| Cache/presence | **Redis Cluster** | Presence, viewer counts, rate limits, hot feed pages, dedupe windows |
| Event bus | **Kafka** | Ordered per-partition event log; replay for analytics and fraud backfill |
| Search | **OpenSearch** | Users, rooms, tags |
| Object storage | **S3-compatible** | Avatars, thumbnails, recordings, evidence, gift assets |
| Media | See doc 09 | LL-HLS/CMAF for 1:many; WebRTC SFU for 1:1 and co-host |
| Mobile | **Kotlin + Compose** / **Swift + SwiftUI** | Native. A 143MB cross-platform bundle with poor lifecycle handling is exactly the failure mode we're avoiding (complaint C1) |
| Web | **React + TypeScript** | |
| Infra | Kubernetes, multi-AZ, IaC | |

**On native vs. cross-platform:** the observed competitor's most-cited crash is an audio-focus/player lifecycle bug during volume adjustment. That class of defect is disproportionately common when media playback is routed through an abstraction layer. Media surfaces get native implementations.

## 5. Request paths

**Read (feed):** client → CDN → gateway → authN → feed module → Redis hot page → miss → Postgres + ranking → cache → response with cursor. Target p99 300ms.

**Write (gift) — the critical path:**
```
client POST /v1/gifts/send {idempotency_key, gift_id, room_id, quantity}
  → gateway: authN, rate limit, schema validate
  → core: authorize (room exists, live, sender not banned/muted, gift valid in region)
  → LEDGER (single serializable tx):
        check idempotency_key → if seen, return original result, DO NOT re-execute
        SELECT balance FOR UPDATE
        if insufficient → abort, return 402 with exact shortfall
        INSERT debit entry (viewer wallet)
        INSERT credit entry (creator diamonds, net of platform share)
        INSERT credit entry (platform revenue)
        INSERT gift_transaction (status=COMPLETED)
        assert sum(entries) == 0
        COMMIT
  → emit Kafka gift.sent
  → realtime gateway fans out GIFT_SENT to room, BALANCE_CHANGED to sender
  → response 200 {transaction_id, new_balance, gift}
```
The client's balance updates **from the response**, never optimistically. If the request times out, the client retries with the same idempotency key and gets the original result. This is how "I bought coins and they vanished" stops being possible.

**Real-time fanout:** producer → Kafka topic partitioned by `room_id` → realtime gateway consumers (each node subscribes only to rooms it holds subscribers for) → per-connection write with per-room sequence number.

## 6. Consistency model

| Data | Model | Why |
|---|---|---|
| Wallet, ledger, orders, payouts | **Strict serializable** | Money |
| Session authorization, age status, blocks | **Strongly consistent** | Safety |
| Follow state | **Read-your-own-writes** | Directly fixes C5 — see doc 06 §4 |
| Follower/following counts | Eventually consistent, ≤5s | Counts can lag; membership cannot |
| Viewer counts | Eventually consistent, approximate >10k | Nobody needs exact |
| Feed ranking | Eventually consistent, ≤60s | |
| Chat messages | Per-conversation ordered, at-least-once + client dedupe | |
| Analytics | Eventually consistent | |

## 7. Failure behavior

| Failure | Behavior |
|---|---|
| Translation provider down | Original text renders; translation marked unavailable. **Never blocks chat** |
| Recommendation engine down | Fall back to recency + region |
| Moderation classifier down | **Fail closed on broadcast and private video** (new sessions blocked); text falls back to a static blocklist |
| Ledger down | **Reject all spend writes.** Reads serve last-committed, labelled stale. Never optimistically credit |
| Media control down | Existing streams continue; new streams blocked; social surfaces unaffected |
| Realtime gateway node loss | Clients reconnect with backoff + jitter; resume from last `server_sequence` |
| Postgres primary failover | Writes pause ~15–30s; clients retry idempotently; no data loss (sync replica) |
| Store billing API down | Orders queue in `PENDING_PAYMENT`; reconciler drains; user sees status, not silence |

Note the asymmetry: **money and safety fail closed; everything else fails open.**

## 8. Multi-region

Phase 1 single region + global CDN. Phase 2 read replicas near users, media edges regional, ledger stays single-writer. Phase 3 regional data residency where required (EU), with the ledger partitioned by residency zone. Cross-region gifting is the hard part and is deliberately deferred — the interim answer is region-local wallets.

## 9. Original design system (brief §O)

**Brand: Lumena.** Everything original — no reuse of the reference product's marks, palette, icons, type, artwork, or motion.

- **Palette:** primary `#4B2AE8` (indigo-violet), accent `#FF5C7A` (coral), success `#12B886`, warn `#F59F00`, danger `#E03131`. Neutrals on a warm-grey ramp. Dark-first, because video is the content and dark chrome disappears behind it.
- **Type:** one variable geometric sans, weights 400/600/800. Tabular figures for all money.
- **Motion:** 180ms standard easing; gift effects capped at 3s and always skippable; `prefers-reduced-motion` fully honored.
- **Iconography:** custom 24px grid, 2px stroke, rounded caps.
- **Gift artwork:** commissioned original, delivered as Lottie/rive with static PNG fallback, streamed on demand and cached — **not bundled**. This is why our binary target is ≤60MB versus the ~143MB observed on the reference app.
- **Accessibility:** WCAG 2.2 AA, 4.5:1 minimum, 44pt targets, full screen-reader labelling, dynamic type.
