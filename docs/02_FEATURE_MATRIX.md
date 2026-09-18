# 02 — Feature Matrix

Legend — **Status:** `NOT IMPLEMENTED` for everything at time of writing (see §Q of the brief; nothing is claimed done that isn't).
**Tier:** `P0` launch-blocking · `P1` launch · `P2` fast-follow · `P3` later.
**Risk:** `S` safety · `M` money · `L` legal · `T` technical.

---

## A. Identity & Access

| ID | Feature | Tier | Risk | Guest? | Notes | Status |
|---|---|---|---|---|---|---|
| ID-01 | Phone OTP auth | P0 | S | — | Primary. Rate-limited, SIM-swap aware | NOT IMPLEMENTED |
| ID-02 | Email + password auth | P0 | — | — | Argon2id | NOT IMPLEMENTED |
| ID-03 | Google / Apple OAuth | P1 | — | — | Apple mandatory if Google is offered on iOS | NOT IMPLEMENTED |
| ID-04 | Guest / anonymous browse | P1 | S | ✅ | Read-only. No chat, no gift, no private, no broadcast | NOT IMPLEMENTED |
| ID-05 | Session & device management | P1 | S | — | List/revoke sessions, per-device tokens | NOT IMPLEMENTED |
| ID-06 | Self-service account recovery | P0 | S | — | **BT-10** | NOT IMPLEMENTED |
| ID-07 | **Age declaration + age assurance** | **P0** | **S,L** | — | **BT-11. Blocks broadcast & private video.** See doc 10 §2 | NOT IMPLEMENTED |
| ID-08 | Account + data deletion (self-serve) | P0 | L | — | GDPR Art.17 / CCPA. 30-day grace, ledger retained pseudonymized | NOT IMPLEMENTED |
| ID-09 | KYC for payout-eligible creators | P0 | M,L | — | Required before any withdrawal. Sanctions/PEP screening | NOT IMPLEMENTED |

## B. Profile & Social Graph

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| SG-01 | Profile view / edit | P0 | — | Avatar moderated before publish | NOT IMPLEMENTED |
| SG-02 | **Follow** | P0 | — | Idempotent | NOT IMPLEMENTED |
| SG-03 | **Unfollow — first-class inverse** | P0 | — | **BT-02.** Never coupled to block | NOT IMPLEMENTED |
| SG-04 | Followers / Following lists | P0 | — | **BT-03** read-your-own-writes | NOT IMPLEMENTED |
| SG-05 | Block / unblock | P0 | S | Orthogonal to follow. Bidirectional invisibility | NOT IMPLEMENTED |
| SG-06 | Report user / room / message | P0 | S | Every surface. ≤2 taps | NOT IMPLEMENTED |
| SG-07 | Mute (soft, one-way) | P1 | — | Distinct from block | NOT IMPLEMENTED |
| SG-08 | Friend / mutual-follow state | P2 | — | Gates some private-chat defaults | NOT IMPLEMENTED |
| SG-09 | Privacy controls (who can DM/call me) | P0 | S | Default: mutuals only for calls | NOT IMPLEMENTED |

## C. Discovery

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| DS-01 | Home feed (personalized) | P1 | — | `RuleBasedRecommendationEngine` | NOT IMPLEMENTED |
| DS-02 | Hot / trending | P1 | — | Velocity-weighted, not raw concurrency | NOT IMPLEMENTED |
| DS-03 | Explore (categories, regions, languages) | P1 | — | | NOT IMPLEMENTED |
| DS-04 | Search (users, rooms, tags) | P1 | — | Debounced, typo-tolerant | NOT IMPLEMENTED |
| DS-05 | **Feed state preservation** | P0 | T | **BT-05** | NOT IMPLEMENTED |
| DS-06 | Nearby / regional | P2 | S,L | Coarse region only — never precise geo | NOT IMPLEMENTED |
| DS-07 | ML ranking | P3 | — | Interface designed now, impl later | NOT IMPLEMENTED |

## D. Live Rooms

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| LR-01 | Watch stream (LL-HLS/WebRTC) | P1 | T | | NOT IMPLEMENTED |
| LR-02 | Broadcast (RTMP/WHIP ingest) | P1 | S | Gated on ID-07 | NOT IMPLEMENTED |
| LR-03 | Room chat | P1 | S | Pre-publish filter | NOT IMPLEMENTED |
| LR-04 | **Chat visibility toggle** | P0 | — | **BT-01** | NOT IMPLEMENTED |
| LR-05 | Likes / reactions | P1 | — | Client-batched, server-capped | NOT IMPLEMENTED |
| LR-06 | Viewer count & viewer list | P1 | — | Debounced; approximate above 10k | NOT IMPLEMENTED |
| LR-07 | Gift send + animation | P1 | M | See §F | NOT IMPLEMENTED |
| LR-08 | **Auto-reconnect w/ backoff** | P0 | T | **BT-04** | NOT IMPLEMENTED |
| LR-09 | Host controls (mute/kick/ban/pin) | P1 | S | | NOT IMPLEMENTED |
| LR-10 | Moderator delegation | P2 | S | | NOT IMPLEMENTED |
| LR-11 | Share / deep link | P2 | — | | NOT IMPLEMENTED |
| LR-12 | PK / co-host battle | P3 | — | Category-standard, later | NOT IMPLEMENTED |
| LR-13 | **Real-time stream moderation (NSFW/CSAM)** | **P0** | **S,L** | Frame sampling → classifier → auto-cut | NOT IMPLEMENTED |
| LR-14 | Picture-in-picture / background audio | P2 | T | Fixes C1-adjacent lifecycle | NOT IMPLEMENTED |

## E. Private Chat & Private Video

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| PC-01 | 1:1 text chat | P1 | S | | NOT IMPLEMENTED |
| PC-02 | Delivery + read receipts | P1 | — | | NOT IMPLEMENTED |
| PC-03 | Typing / presence | P2 | — | Presence is opt-out | NOT IMPLEMENTED |
| PC-04 | Attachments (image/audio) | P2 | S | Scanned before delivery | NOT IMPLEMENTED |
| PC-05 | Message delete (self / both) | P1 | — | Tombstone, audit-retained | NOT IMPLEMENTED |
| PV-01 | **Private video session** | P1 | **S,M** | Dedicated architecture, doc 09 §5 | NOT IMPLEMENTED |
| PV-02 | Per-minute billing w/ pre-auth | P1 | M | Hold → meter → settle | NOT IMPLEMENTED |
| PV-03 | Consent + authorization gate | P0 | S | Both parties, both age-verified | NOT IMPLEMENTED |
| PV-04 | In-call report / end / block | P0 | S | One tap, always visible | NOT IMPLEMENTED |
| PV-05 | Session timeout & reconnect grace | P1 | M | Billing pauses on disconnect | NOT IMPLEMENTED |
| PV-06 | Screenshot/recording deterrence | P2 | S | FLAG_SECURE + notify peer. Deterrent only — documented as such | NOT IMPLEMENTED |
| PV-07 | **CSAM detection in private sessions** | **P0** | **S,L** | Non-negotiable. Doc 10 §2 | NOT IMPLEMENTED |

## F. Economy

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| EC-01 | Coin purchase via **store IAP only** | P0 | M,L | Google Play Billing / StoreKit. **No off-platform QR rails** — closes C7 | NOT IMPLEMENTED |
| EC-02 | Server-authoritative wallet | P0 | M | Client never asserts balance | NOT IMPLEMENTED |
| EC-03 | Double-entry ledger | P0 | M | Doc 11 | NOT IMPLEMENTED |
| EC-04 | Idempotent gift send | P0 | M | `idempotency_key` required | NOT IMPLEMENTED |
| EC-05 | Purchase reconciliation job | P0 | M | **BT-06** | NOT IMPLEMENTED |
| EC-06 | **User-visible transaction history** | P0 | M | **BT-07** | NOT IMPLEMENTED |
| EC-07 | Creator diamond ledger | P1 | M | Separate from viewer wallet | NOT IMPLEMENTED |
| EC-08 | Payout / withdrawal | P1 | M,L | Gated on ID-09 KYC | NOT IMPLEMENTED |
| EC-09 | Refund & chargeback handling | P0 | M,L | Reversal entries, never balance edits | NOT IMPLEMENTED |
| EC-10 | Spend limits & cooling-off | P1 | S | Self-set caps; category harm mitigation | NOT IMPLEMENTED |
| EC-11 | Gift catalogue (original artwork) | P1 | — | | NOT IMPLEMENTED |
| EC-12 | Daily check-in / free drip | P2 | — | | NOT IMPLEMENTED |

## G. Match

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| MT-01 | Match request → candidate → session | P2 | S | Server-controlled | NOT IMPLEMENTED |
| MT-02 | Preference filters | P2 | S,L | Gender filters only where lawful; see doc 10 §7 | NOT IMPLEMENTED |
| MT-03 | Match decision + history | P2 | — | | NOT IMPLEMENTED |
| MT-04 | Anti-abuse in match queue | P0 | S | Blocklist honored, repeat-offender exclusion | NOT IMPLEMENTED |

## H. Translation

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| TR-01 | `TranslationProvider` interface | P1 | — | | NOT IMPLEMENTED |
| TR-02 | `DevTranslationProvider` (**DEV MOCK**) | P1 | — | Clearly marked, never ships | NOT IMPLEMENTED |
| TR-03 | `ProductionTranslationProvider` | P1 | — | Vendor-pluggable | NOT IMPLEMENTED |
| TR-04 | Async non-blocking chat translation | P1 | T | Original renders first, always | NOT IMPLEMENTED |
| TR-05 | Per-user language prefs + toggle | P1 | — | | NOT IMPLEMENTED |
| TR-06 | Translation cache | P2 | — | Hash-keyed | NOT IMPLEMENTED |
| TR-07 | Live speech translation (captions) | P3 | — | Explicitly out of scope for v1 | NOT IMPLEMENTED |

## I. Trust, Safety & Moderation

| ID | Feature | Tier | Risk | Notes | Status |
|---|---|---|---|---|---|
| TS-01 | Report intake + triage queue | P0 | S | | NOT IMPLEMENTED |
| TS-02 | Text filter (pre-publish) | P0 | S | | NOT IMPLEMENTED |
| TS-03 | Image/video classifier | P0 | S | | NOT IMPLEMENTED |
| TS-04 | **CSAM hash-match + NCMEC reporting** | **P0** | **S,L** | Legal obligation, not a feature | NOT IMPLEMENTED |
| TS-05 | **Enforcement w/ stated reason + appeal** | P0 | S | **BT-09.** DB-enforced | NOT IMPLEMENTED |
| TS-06 | Appeals workflow + case IDs | P0 | S | | NOT IMPLEMENTED |
| TS-07 | Human review tier | P0 | S | No fully-automated permanent bans | NOT IMPLEMENTED |
| TS-08 | Moderator console | P1 | S | | NOT IMPLEMENTED |
| TS-09 | Transparency reporting | P2 | L | | NOT IMPLEMENTED |
| TS-10 | Grooming-pattern detection | P1 | S | Adult→minor contact signals | NOT IMPLEMENTED |

## J. Anti-Fraud

| ID | Feature | Tier | Notes | Status |
|---|---|---|---|---|
| AF-01 | `risk_score` / `risk_reasons` / `review_status` | P0 | Never auto-ban on one signal | NOT IMPLEMENTED |
| AF-02 | Device + IP fingerprint clustering | P1 | Multi-account detection | NOT IMPLEMENTED |
| AF-03 | Gift-loop / circular-flow detection | P1 | A→B→A earnings washing | NOT IMPLEMENTED |
| AF-04 | Velocity limits | P0 | Signup, gift, message, call | NOT IMPLEMENTED |
| AF-05 | Chargeback & refund-abuse tracking | P1 | | NOT IMPLEMENTED |
| AF-06 | Payout hold rules | P1 | Clawback window before withdrawal | NOT IMPLEMENTED |
| AF-07 | Bot/automation detection | P2 | | NOT IMPLEMENTED |

## K. Platform & Ops

| ID | Feature | Tier | Notes | Status |
|---|---|---|---|---|
| PL-01 | Push notifications | P1 | Per-category opt-out | NOT IMPLEMENTED |
| PL-02 | In-app notification center | P1 | | NOT IMPLEMENTED |
| PL-03 | **In-app support w/ ticket IDs** | P0 | **BT-08** | NOT IMPLEMENTED |
| PL-04 | Analytics pipeline | P1 | | NOT IMPLEMENTED |
| PL-05 | Feature flags / remote config | P1 | | NOT IMPLEMENTED |
| PL-06 | Crash + ANR reporting | P0 | Closes C1 class | NOT IMPLEMENTED |
| PL-07 | Creator dashboard | P2 | | NOT IMPLEMENTED |
| PL-08 | Admin console | P2 | | NOT IMPLEMENTED |
| PL-09 | i18n / RTL | P1 | | NOT IMPLEMENTED |
| PL-10 | Accessibility (TalkBack/VoiceOver, contrast, captions) | P1 | | NOT IMPLEMENTED |

---

## Launch gate

**A build cannot go to production while any `P0 / Risk:S` row is NOT IMPLEMENTED.** In particular: ID-07, LR-13, PV-03, PV-07, TS-04, TS-05 gate the *entire* private-video and broadcast surface. If schedule pressure hits, the correct cut is **ship watch-only + chat and defer broadcast and private video** — not ship them unguarded.
