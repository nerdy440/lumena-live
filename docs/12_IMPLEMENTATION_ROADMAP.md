# 12 — Implementation Roadmap

All phases are `NOT IMPLEMENTED` at time of writing.

---

## 0. Phase 0 — Safety Infrastructure (prerequisite to launch)

**This phase has no "skip" option. Phase 1 does not ship to production until Phase 0 is complete.**

Phase 0 does not ship a product. It builds the infrastructure that makes shipping safe.

| Component | Owner area | Notes |
|---|---|---|
| NCMEC CyberTipline API integration | Moderation | Legal obligation, not optional |
| PhotoDNA / CSAM hash-matching pipeline | Moderation | Integrates with NCMEC |
| Age declaration flow (AUTH_008) | Identity | Hard floor — under-minimum is unrecoverable at account level |
| Age assurance vendor integration (AUTH_009) | Identity | Document verification vendor |
| Moderation rule catalogue with stable IDs | T&S | `moderation_rules` table + public policy page |
| Enforcement schema with `rule_id NOT NULL` | T&S | DB constraint before first enforcement |
| Appeal intake (MODR_003) | T&S | Before first enforcement can be issued |
| In-app support tickets (SUPP_002/003) | Support | Before any money moves |
| Spend-limit infrastructure (SETG_004) | Economy | Before any IAP |
| Legal review — privacy policy, terms, age policy | Legal | Per region of initial launch |

**Exit gate for Phase 0:** legal sign-off on privacy policy + terms. NCMEC integration tested end-to-end with test hashes. Age assurance vendor SLA confirmed. At least one enforcement can be issued, viewed, and appealed end-to-end.

---

## Phase 1 — Product Architecture (from the brief)

**Week 1–2.** Sets the foundation that every subsequent phase builds on.

Deliverables:
- Monorepo structure (API, realtime gateway, ledger service, mobile SDKs)
- DB schema: all tables from doc 06 (DDL committed, migrations in place)
- Local dev environment: docker-compose with Postgres, Redis, Kafka, S3-compatible
- CI pipeline: lint, test, build, migration check
- Feature flag system
- Observability: structured logging, distributed tracing, metrics dashboards
- Config management: secrets out of code, environment-specific config

**Exit gate:** CI is green. `docker-compose up` produces a running system. No hardcoded secrets in the codebase. All 12 planning documents reviewed and internally consistent (this review).

---

## Phase 2 — Authentication

**Week 3–4.**

- Phone OTP (ID-01)
- Email/password (ID-02)
- Session management, refresh rotation, reuse detection
- Age declaration (AUTH_008) — hard-stop for under-minimum
- Self-service account recovery (ID-06)
- Account deletion (ID-08) with grace period

**NOT in this phase:** OAuth (ID-03), age assurance (ID-07), KYC (ID-09). These depend on vendor integrations that run in parallel.

**Exit gate:** 100% of auth paths integration-tested. Rate limits verified. Age hard-stop verified (create under-age account, confirm every gated surface returns `UNDER_AGE_MINIMUM`). Token reuse detection triggers family revocation. No secret is logged.

---

## Phase 3 — User / Profile / Social Graph

**Week 5–6.**

- Profile CRUD with avatar moderation queue (SG-01)
- **Follow and unfollow as independent operations (SG-02, SG-03)** — the C4 fix
- **Read-your-own-writes on following list (SG-04)** — the C5 fix
- Block / unblock, separate from follow (SG-05)
- Mute (SG-07)
- Privacy settings with safe defaults (SG-09)
- Profile view: guests see public info; blocked-by renders generic unavailable

**Exit gate:** write follow → immediately visible in own list (primary read). Write unfollow → followee gone from list within one read. Block has zero interaction with follow table. Avatar queue rejects test NSFW images. Privacy defaults are correct (who_can_call = mutuals).

---

## Phase 4 — Home / Hot / Explore

**Week 7–8.**

- Feed API with cursor pagination (DS-01–DS-04)
- `RuleBasedRecommendationEngine` — freshness + engagement + gift activity + language + region
- Feed state persistence — cursor + scroll offset survive round trip to room and process death (BT-05)
- Search: user, room, tag
- Guest browsing (read-only, all discovery surfaces)
- Notification center (unread counts, summary)

**Exit gate:** feed state is restored correctly (integration test: load page 2, navigate away, return, assert same cursor). Search returns results within 200ms p95. Guest can browse without auth prompt until a gated action is taken.

---

## Phase 5 — Live Streaming

**Week 9–12.**

- WHIP ingest integration (broadcaster SDK — native Kotlin + Swift)
- Transcoder ladder deployment
- LL-HLS packaging + CDN integration
- Viewer playback (native player, ABR, reconnect)
- **Audio-focus lifecycle fix (BT-12):** correct teardown order, stress-tested
- Broadcast creation and pre-live setup (ROOM_003), **gated on age assurance**
- Age assurance vendor integration (ID-07, AUTH_009)
- Stream key generation (single-use, short-lived)
- Basic room API (join, leave, viewer count)

**Exit gate:** 500 concurrent test streams without ingest node saturation. Viewer p99 start latency <4s. Volume stress test: 1000 volume key events during playback, zero crashes. Age-assurance gate: attempt to create a broadcast without assurance, confirm 403. Stream terminates within 5s of `POST /streams/{id}/stop`.

---

## Phase 6 — Live Room Real-time Engine

**Week 13–15.**

- WebSocket gateway with Kafka fan-out
- Room subscription, backfill, gap detection
- Chat: COMMENT, pre-publish text moderation, seq ordering
- **Chat visibility toggle (BT-01)**
- Likes (batched client-side, server-capped)
- Follow event in room
- Viewer joined/left events
- GIFT_SENT event (no economy yet — event only, gift button says "Coming soon")
- Real-time visual moderation pipeline (frame sampling → classifier → auto-terminate)
- **CSAM hash-matching on live streams**
- Reconnect with backoff and backfill (BT-04)
- Host moderation controls (mute, kick) → USER_MUTED/KICKED events
- Enforcement events with rule ID (ACCOUNT_STATUS on WS)

**Exit gate:** kill WebSocket mid-stream, confirm auto-reconnect within 5s, confirm chat backfilled, confirm no duplicate messages. Inject a test CSAM hash into a stream, confirm stream terminates and report is filed. Chat toggle persists across room exit/return.

---

## Phase 7 — Chat (Private)

**Week 16–17.**

- Conversation list with unread counts (CHAT_001)
- 1-to-1 message delivery with server seq (CHAT_002)
- Delivery + read receipts
- Typing presence (opt-out)
- Message delete (tombstone, not hard delete)
- Request folder (non-mutual messages)
- Block from conversation
- Report from conversation

**Exit gate:** message deduplication on retry (same `client_msg_id`, confirm single delivery). Deletion tombstones both sides. Blocked user cannot message. Request folder correctly separates non-mutuals.

---

## Phase 8 — Gifts

**Week 18–19.**

- Gift catalogue (original artwork, Lottie/Rive, streamed not bundled)
- Gift send with idempotency (EC-04)
- Gift drawer with balance display (ROOM_006)
- Gift animation layer (GPU-composited, skippable, reduced-motion aware)
- GIFT_SENT room event, BALANCE_CHANGED private event
- Error handling: insufficient balance → top-up sheet pre-filled with shortfall
- **No optimistic balance update** — balance updates only from server response

**Exit gate:** duplicate send with same idempotency key returns one transaction, charges once. Network failure mid-send → retry delivers one gift. Insufficient balance: balance unchanged, shortfall correct, top-up sheet pre-filled. Gift animation is skippable. Animation degrades to static on low-end devices.

---

## Phase 9 — Wallet / Ledger

**Week 20–22.**

- Coin purchase via store IAP only — **no off-platform rails (EC-01)**
- Server-side purchase token verification
- Order status machine (CREATED→CREDITED) (EC-05)
- Reconciler background worker (pending orders auto-heal within 60s)
- **Full user-visible transaction ledger (EC-06, BT-07)**
- Pending order banner in wallet (BT-06)
- Spend limits and cooling-off (EC-10)
- Nightly ledger integrity check (sum to zero assertion)

**Exit gate:** kill server after store purchase but before credit; confirm reconciler credits within 60s and user sees status, not silence. Verify that purchase token cannot be replayed to a second account. Confirm balance never goes negative (attempt via race condition). Full transaction history is visible per BT-07.

---

## Phase 10 — Private Messaging

**Week 23.**

- Attachment upload (presigned S3, scanned before delivery) (PC-04)
- Attachment viewer (CHAT_004)
- Full message request flow

**Exit gate:** attachment is not deliverable until scan completes. Malicious attachment (EICAR test) is blocked.

---

## Phase 11 — Private Video

**Week 24–27.** Longest phase due to safety complexity.

- PV session architecture (doc 09 §5)
- **Full gate chain, server-side (10 §4)**
- WebRTC SFU / TURN integration
- Billing: pre-authorization hold, active interval tracking, settlement
- **CSAM detection in private sessions (PV-07)**
- In-call controls: End, Report (one tap), Block
- PV_BILLING_TICK live meter
- Low-balance warning + graceful end
- Session reconnect (billing pauses, resumes)
- Call history (PVID_004)
- Incoming call (PVID_002)
- Session summary with cost (PVID_003)

**Exit gate:** gate chain test (6 distinct failure cases each produce correct behavior). Billing: kill network during active call, reconnect, confirm billing paused during disconnect. Settlement test: compare consumed_coins to sum of active_intervals. CSAM test hash in session → immediate termination + report filed. Caller cannot spend more than authorized hold.

---

## Phase 12 — Matchmaking

**Week 28–29.**

- Match request → candidate → session state machine (MT-01)
- Server-controlled matching — no client selection of candidates
- Match exclusions: blocked users, reported users, recent skips
- Preference filters (MT-02) — legal review per region
- Match history (MT-03)
- Anti-abuse: rate limits, repeat-offender exclusion

**Exit gate:** blocked user never appears as a candidate in either direction. `score_reasons` is not in any API response (serializer test). Gender filtering is disabled by default and can only be enabled region-by-region via feature flag.

---

## Phase 13 — Translation

**Week 30.**

- `TranslationProvider` interface
- `DevTranslationProvider` — **DEV MOCK, clearly marked, never ships** (returns `[MOCK: ${original}]`)
- `ProductionTranslationProvider` — vendor pluggable
- Async COMMENT_TRANSLATED events (original renders first, translation patches in)
- Translation toggle per room, per-user preference
- Language detection on incoming messages

**Exit gate:** translation failure does not block or delay original message delivery. Translation toggle state persists. `DevTranslationProvider` is excluded from production build by build flag.

---

## Phase 14 — Creator Dashboard

**Week 31–32.**

- Earnings ledger (CRTR_002)
- Analytics (stream stats, follower growth, gift breakdown)
- Payout request with KYC gate (CRTR_003, ID-09)
- Creator-mode profile display

**Exit gate:** payout blocked without KYC. Payout blocked within hold period. Earnings reconcile to gift + PV settlement transactions.

---

## Phase 15 — Moderation

**Week 33–34.**

- Moderator console (MODR_001 et al.)
- Report triage queue
- Human review for escalated auto-flags
- Bulk tools for room moderation
- Grooming-pattern detection signals

**Exit gate:** auto-flagged account reaches a human reviewer within SLA. No permanent termination via automated action (constraint verified). Reviewer sees the evidence reference.

---

## Phase 16 — Admin Dashboard

**Week 35.**

- Account management (view, restrict, unsuspend)
- Economy oversight (balance anomalies, gift loops)
- Fraud review queue
- Support ticket escalation
- Platform health dashboards

**Exit gate:** admin cannot perform actions outside their role (RBAC enforced). Admin actions are audit-logged. Admin cannot edit ledger entries directly.

---

## Phase 17 — Analytics

**Week 36.**

- Event pipeline: Kafka → analytics warehouse
- Standard dashboards: DAU/MAU, retention, conversion, economy
- Creator analytics
- Funnel analysis (onboarding, first gift, first broadcast)

**Exit gate:** dashboard data matches production counts within 1% (sampling validation).

---

## Phase 18 — Fraud Prevention

**Week 37–38.**

- Risk scoring pipeline (AF-01)
- Device + IP clustering (AF-02)
- Gift loop detection (AF-03)
- Velocity limits already deployed (AF-04) — this phase adds tuning and alerts
- Chargeback tracking (AF-05)
- Payout hold rules (AF-06)

**Exit gate:** gift loop test case (A→B, B→A within 5 minutes) elevates risk score. Multi-account test (same device, multiple accounts) clusters correctly. No automatic permanent ban from risk score alone — verified by policy test.

---

## Phase 19 — Performance Testing

**Week 39.**

- Load: 500 concurrent broadcasts, 50k viewers, 10k WS connections
- Stress: 2× target load for 30 minutes
- Soak: target load for 72 hours
- Chaos: kill random instances, confirm auto-recovery; kill Postgres primary, confirm failover; kill one Kafka broker, confirm no event loss

**Exit gate:** all performance targets from doc 09 §8 met. Chaos tests pass with defined RTO.

---

## Phase 20 — Production Deployment

**Week 40–42.**

- Staged rollout: internal → 1% → 5% → 25% → 100%
- Canary metrics: error rate, p99 latency, gift success rate, crash rate
- Rollback runbook tested
- On-call rotation and incident runbook
- Legal sign-off by launch region

---

## Decision gates summary

| Gate | Blocks |
|---|---|
| Phase 0 complete + legal sign-off | Phase 5 (broadcast) launch |
| Age assurance integration tested | Phase 5 (broadcast) launch |
| CSAM pipeline end-to-end tested | Phase 6 (room real-time) launch |
| Ledger integrity verified | Phase 9 (wallet) launch |
| Full PV gate chain tested | Phase 11 (private video) launch |
| Fraud rules reviewed by legal | Phase 18 (fraud) launch |
| Performance tests pass | Phase 20 (production) |

---

## Definition of Done (universal)

A feature is complete only when all of these hold:

- [ ] UI exists
- [ ] Backend exists
- [ ] Database schema exists and is migrated
- [ ] API endpoint exists and is documented
- [ ] Authentication check exists
- [ ] Authorization check exists
- [ ] Error handling exists (specific messages, not generic)
- [ ] Loading state exists (skeleton, not blank)
- [ ] Empty state exists (illustration + action)
- [ ] At least one integration test exists
- [ ] Logging exists (structured, no PII in logs)
- [ ] Analytics events are fired
- [ ] Security review completed (per doc 10 §9 checklist)
- [ ] Documentation updated
