# 01 — Product Research

**Subject of analysis:** ELive-Chill&Stream (`com.elive.joy.android`), publisher listed variously as "Joywave Dev Team" / "Efun Team", web surface `app.elive.fans`.
**Our product:** codename **Lumena Live** (`app.lumena.live`) — original brand, original code, original assets.
**Method:** black-box. Public listing copy, public store metadata, public user reviews, public category analysis.

---

## 0. Epistemic status — read this first

This document separates three tiers of claim. Nothing is promoted between tiers without evidence.

| Tier | Meaning | How it may be used |
|---|---|---|
| **OBSERVED** | Stated in public material we retrieved | Cite freely |
| **INFERRED** | Category-standard design that would produce the observed behavior | Design input only; never stated as fact about ELive |
| **UNKNOWN** | Not observable without decompilation or private API access | Must be designed from first principles |

**What we did NOT do, deliberately:** no APK decompilation, no private/undocumented API probing, no traffic interception of their service, no extraction of assets, gift animations, certificates, keys, or code. We did not attempt to enumerate their endpoints. Everything below the OBSERVED tier is our own engineering, not their engineering recovered.

**Consequence for the rest of the doc set:** every screen, event, table, and endpoint in docs 03–12 is an original design. Where a spec looks like it "describes ELive," it does not — it describes Lumena, designed to satisfy the same user-visible job.

---

## 1. OBSERVED — feature claims from public listing

Directly stated in the app's own public store copy:

- **Live streaming rooms at scale**, segmented local vs. global. Marketing claims "thousands of rooms."
- **Real-time translation** positioned as the mechanism that "removes language barriers" between hosts and viewers.
- **Virtual gifts** with visual effects, framed as the appreciation/support mechanic.
- **Private text and private video chat** with hosts ("anchors") or friends.
- **Privacy protection** as a marketing pillar (unspecified mechanism).
- Vocabulary: creators are called **"anchors"** — a term characteristic of the Asian live-social lineage (YY / Momo / Bigo), not the Western creator-platform lineage (Twitch / TikTok Live).

Public metadata:

- ~5.3M lifetime installs, ~11k/day in a recent 30-day window; ranked highly in the Lifestyle category.
- Rating ≈ 3.1/5 on ~6.5k ratings. **This is the single most informative number in the research.** A 3.1 at 5M installs is not a bad product — it is a *polarized* product: a satisfied spending core and a large tail of users with unresolved money and moderation grievances.
- Android 6.0+ minimum, ~143MB download. Large binary for a streaming client ⇒ **INFERRED**: bundled gift animation assets (likely SVGA/PAG/Lottie sequences) shipped in-package rather than streamed on demand.
- Listed content rating: High Maturity in some regions, with reviewers actively disputing a "Teen" rating elsewhere.
- Support channel is a single email address (`service@elive.fans`) plus in-app contact; developer replies to negative reviews are templated and route everything back to that address.

**Third-party listing prose is not evidence.** Aptoide/Softonic/APK-mirror descriptions of "privacy protection integrated throughout the application's architecture" are SEO rewrites of the store blurb, not independent findings. Discarded.

---

## 2. OBSERVED — user complaints, verbatim themes

This is the highest-value input in the entire research. Each is a *paid* signal: someone was annoyed enough to write it.

| # | Observed complaint | Engineering diagnosis (ours) |
|---|---|---|
| C1 | App crashes when adjusting volume during a live | Media/audio-focus handling on a background thread, or a volume-key interceptor racing the player. Classic Android `AudioManager` + player-release race. |
| C2 | Leaving a live resets the feed — "I lose my place" | Feed state destroyed on navigation. No cursor persistence, no scroll restoration, no back-stack retention. |
| C3 | Chat overlay cannot be hidden or dismissed | No chat visibility control; overlay is structurally welded to the room view. |
| C4 | **Unfollow is only possible by blocking** | Follow is a one-way write with no inverse operation exposed. This is a data-integrity smell, not a UI gap. |
| C5 | **Followed users don't appear in the Following tab** | Follow write and follow-list read are inconsistent — different stores, or an async projection that never converges. |
| C6 | Bought coins never arrived | Payment capture and wallet credit are not one atomic outcome. No visible reconciliation job, no user-facing receipt/status. |
| C7 | Third-party payment rail (PhonePe QR) failing; users calling it fraud and escalating to national cybercrime portals | Off-platform payment collection with no order binding. Whether or not fraud is occurring, **the architecture makes it indistinguishable from fraud**, which is functionally identical from the user's seat. |
| C8 | Bans with no stated reason and no appeal path | No moderation transparency, no appeals workflow, no case ID. |
| C9 | Streamers banned "for lots of reasons," inconsistently | Enforcement thresholds not published; likely automated with no human review tier. |
| C10 | Explicit content on ordinary streams; **reviewers reporting apparent minors** | Insufficient age assurance and insufficient real-time visual moderation. |
| C11 | Coin pricing perceived as expensive; slow level progression | Economy tuning — noted, not a defect. |
| C12 | Unverified claims of personal-data compromise | Unsubstantiated; treated only as evidence that users *feel* the app is not trustworthy with their data. |

**Cluster analysis.** The complaints fall into exactly four buckets, and three of them are architectural rather than cosmetic:

1. **Client state management** (C1, C2, C3) — presentation layer.
2. **Social graph consistency** (C4, C5) — *the write path and read path disagree*. This is a distributed-data defect.
3. **Money integrity and visibility** (C6, C7) — *payment and wallet are not one transaction*. This is the most serious class.
4. **Trust and safety** (C8, C9, C10, C12) — no transparency, no appeal, no age assurance.

Buckets 2 and 3 are the same underlying failure in two places: **a state change is acknowledged to the user before it is durably and consistently committed.** Lumena's architecture is organized primarily around not doing that.

---

## 3. OBSERVED — category context

Independent category analysis of social live-streaming apps (Bigo Live, LiveMe, Mico, Poppo, StreamKar and peers) finds a consistent pattern: lightly-moderated live video combined with a gifting incentive attracts explicit content, harassment and predatory behavior; weak age verification means minors both appear on and use these apps; and the combination of real-money gifting with inadequate safety controls is the most serious complaint raised, because the harm extends beyond financial loss into exploitation. Coin traps, bot or fake hosts, refused refunds, and accounts banned with the balance still on them are the recurring one-star themes across the whole category.

**This is not an ELive-specific finding. It is a category-structural finding, and it applies to us the moment we ship.** We are not building "a nicer ELive." We are building a product in a category with a documented child-safety failure mode, and the design must assume that adversary from day one.

---

## 4. INFERRED — probable product model

Design inputs only. Not claims about ELive.

- **Two-sided marketplace.** Viewers (spend) and anchors (earn). Likely agency/guild layer above anchors — standard in this lineage, and it explains "hard to earn," ban sensitivity, and quota pressure in reviews.
- **Coin/diamond split economy.** Purchased coins (viewer-side, non-redeemable) vs. earned diamonds (creator-side, redeemable at a platform-set rate). Universal in this category and the only sane way to run it.
- **Engagement-first ranking.** Sorted primarily by concurrency and gift velocity, with regional segmentation.
- **Private video as the monetization peak.** Per-minute billed 1-to-1 video is the highest-ARPU surface in this category and the highest-risk surface simultaneously.
- **Aggressive retention loops.** Daily check-in, free token drips, level progression — confirmed adjacent by reviews mentioning daily check-in rewards and level grind.

## 5. UNKNOWN — must be designed from scratch

Media transport and codec strategy; SFU vs. CDN split; signaling protocol; DB engines; sharding; recommendation model; anti-fraud rules; translation vendor; ledger design; regional data residency. **All of docs 05–11 are original work.** No part of them is recovered from the target.

---

## 6. "Better Than ELive" requirements (traceable)

Each maps to an observed complaint and to a Definition-of-Done gate. These are contractual, not aspirational.

| ID | Requirement | Closes | Acceptance test |
|---|---|---|---|
| **BT-01** | Chat overlay has a persistent user-controlled visibility toggle (hide / opacity / size), persisted per-device | C3 | Toggle survives app restart and room switch |
| **BT-02** | Unfollow is a first-class inverse of follow, reachable everywhere follow is, and **never** requires blocking | C4 | `POST /follow` then `DELETE /follow` returns list to prior state; block and follow are orthogonal axes |
| **BT-03** | Follow state is read-your-own-writes consistent; Following list reflects a follow within 1s p99 | C5 | Write-then-read integration test; no eventual-consistency window exposed to the writer |
| **BT-04** | Stream reconnects automatically with exponential backoff and preserves chat scrollback; explicit visible connection state | — | Kill network 5s mid-stream → auto-resume, no user action |
| **BT-05** | Feed cursor, scroll offset and tab state survive room entry/exit and process death | C2 | Enter room from position 47, exit → still at 47 |
| **BT-06** | **Payment and wallet credit are one atomic outcome.** Every purchase yields an order ID, a visible status machine, and automatic reconciliation. No off-platform QR rails. | C6, C7 | Kill server between capture and credit → reconciler settles within 60s, user sees `PROCESSING` not silence |
| **BT-07** | Full transaction ledger visible to the user: every debit, credit, gift, refund, with ID and timestamp, exportable | C6 | Every balance delta traceable to a user-visible row |
| **BT-08** | In-app support with ticket IDs, status, SLA timer — not a bare email address | support | Ticket created in-app returns an ID immediately |
| **BT-09** | **Moderation transparency:** every enforcement action states the rule violated, the evidence window, duration, and an appeal route with a case ID | C8, C9 | No enforcement can be written without a `rule_id` and `evidence_ref` — enforced at the DB constraint level |
| **BT-10** | Self-service account recovery and self-service account+data deletion | — | Recovery without contacting support |
| **BT-11** | **Age assurance before any 1-to-1 video or any broadcast**, plus real-time nudity/CSAM classification and mandatory NCMEC reporting | C10 | See doc 10 §2. Hard launch blocker. |
| **BT-12** | Audio focus and player lifecycle handled on main thread with proper release ordering; volume never touches player teardown | C1 | Volume stress test during playback, 1000 iterations, zero crashes |

---

## 7. What we will not copy

Not the ELive name, logo, wordmark, icon set, color system, typography, screenshots, artwork, gift animations, sound design, or any string of their UI copy. Not their app-store description. Not their gift catalogue names or visual designs. Lumena's brand, tokens, iconography and gift artwork are original (doc 05 §9, and the design-system spec).

We are copying **product category and user job**, which is not protectable, and we are copying **their bug list**, which is a gift.
