# 04 — Navigation Graph

Three ideas do the work here: **gates** (conditions that must hold before a route resolves), **state preservation contracts** (which routes must survive round trips — the fix for complaint C2), and **modality** (what is a destination vs. an overlay).

---

## 1. Top-level graph

```
AUTH_001 Splash
  ├─[valid session]──────────────────────────────► ROOT (tabs)
  ├─[no session]─────────────────────────────────► AUTH_002 Welcome
  ├─[force update]───────────────────────────────► BLOCKING Update
  └─[region unavailable]─────────────────────────► BLOCKING Region

AUTH_002 Welcome
  ├── AUTH_003 Phone ──► AUTH_004 OTP ──┐
  ├── AUTH_005 Email ───────────────────┤
  ├── AUTH_007 OAuth ───────────────────┤
  │                                     ▼
  │                            [new account?]
  │                              │yes        │no
  │                              ▼           ▼
  │                        AUTH_008 Age    ROOT
  │                              │
  │                     [under minimum]──► HARD STOP (terminal, non-recoverable)
  │                              │
  │                              ▼
  │                        AUTH_010 Profile ─► AUTH_011 Interests ─► AUTH_012 Suggestions ─► ROOT
  │
  └── Continue as guest ─────────────────► ROOT (guest mode)
```

```
ROOT — bottom tab bar (5)
 ├─ TAB 1  HOME_001  For You │ HOME_002 Hot │ DISC_001 Explore │ DISC_004 Following
 ├─ TAB 2  DISC_003  Search
 ├─ TAB 3  [+]       Go Live  → ROOM_003 pre-live   (gate: age-assured + T&S clear)
 ├─ TAB 4  CHAT_001  Messages   (badge: unread)
 └─ TAB 5  PROF_001  Me
```

`MTCH_001 Match` is reached from a Home header entry point rather than a sixth tab. Rationale: a five-tab bar is the accessibility ceiling on small devices, and Match is age-gated — putting a gated destination in the permanent chrome means most sessions show a tab that dead-ends.

---

## 2. Live room subgraph

```
Feed card ──► ROOM_001 Live Room (Viewer)
                │
                ├─ overlay ─► ROOM_006 Gift drawer ──► WALL_002 Top-up sheet
                ├─ overlay ─► ROOM_007 Viewer list ──► ROOM_008 Mini profile
                ├─ overlay ─► ROOM_008 Mini profile ─┬─► PROF_001 Profile
                │                                    ├─► CHAT_002 Conversation
                │                                    └─► MODR_001 Report
                ├─ push ────► PROF_001 Host profile
                ├─ push ────► CHAT_002 Private chat with host
                ├─ push ────► PVID_001 Private video   (gate chain below)
                ├─ modal ───► MODR_001 Report
                ├─ vertical swipe ─► ROOM_001 (next room, same route, state swap)
                └─ back ────► RESTORE feed position   ◄── BT-05 contract
```

**Swipe-to-next is a state swap, not a push.** The back stack must not accumulate one entry per room swiped; otherwise back-from-room-20 walks the user through 19 dead rooms. One `ROOM_001` instance, room ID as swappable state, back always returns to the originating feed.

---

## 3. Private video gate chain

Every gate is evaluated **server-side** at request time. Client-side checks exist only to avoid a pointless round trip and are never trusted.

```
PV entry (profile CTA · room CTA · match connect)
   │
   ├─[G1] viewer age-assured? ────────────no──► AUTH_009 Age assurance
   ├─[G2] callee age-assured? ────────────no──► BLOCK (generic unavailable — never reveal callee state)
   ├─[G3] block relation either way? ─────yes─► BLOCK (generic unavailable)
   ├─[G4] callee privacy allows caller? ──no──► BLOCK (generic unavailable)
   ├─[G5] either under T&S restriction? ──yes─► BLOCK (restricted party sees reason; other sees generic)
   ├─[G6] sufficient balance for minimum? no──► WALL_002 Top-up (exact shortfall shown)
   │
   ▼
POST /v1/private-video/requests   → state REQUESTING
   │
   ▼
PVID_002 Incoming call (callee)  → state RINGING
   ├─ decline ─► end (no charge)
   ├─ timeout ─► end (no charge)
   ├─ block ───► end + block written
   └─ accept ──► AUTHORIZING → billing hold placed → CONNECTING → PVID_001 ACTIVE
                                     │
                                     ├─ end (either party) ─► settle ─► PVID_003 Summary
                                     ├─ peer drop >45s ─────► settle at consumed ─► PVID_003
                                     ├─ balance exhausted ──► graceful end ─► PVID_003 + top-up CTA
                                     └─ safety termination ─► immediate end ─► MODR_002 notice
```

**G2–G5 all render the same generic message.** Distinguishing "they blocked you" from "they're restricted" from "their settings don't allow this" leaks state and is itself an abuse vector — it lets a harasser confirm they've been blocked and rotate accounts.

---

## 4. Money subgraph

```
Any balance-touching surface ──► WALL_002 Top-up sheet
                                    │
                                    ├─ store billing (native, out of process)
                                    │        │
                                    │        ▼
                                    │   POST verify  ──┬─ CREDITED ──► return to origin, balance updated
                                    │                  ├─ PENDING ───► WALL_001 Wallet w/ live order status
                                    │                  └─ FAILED ────► error + SUPP_003 path
                                    └─ cancel ─► return to origin, no state change

PROF_001 Me ──► WALL_001 Wallet ──┬─► WALL_003 Transaction detail ──► SUPP_003 Dispute (prefilled)
                                  ├─► WALL_002 Top-up
                                  └─► SETG_004 Spend limits
```

**Rule: the top-up sheet always returns to its origin.** A user topping up mid-stream to send a gift must land back in the room with the gift drawer still open and their selection intact. Dumping them to the wallet after purchase is how you lose the gift *and* the goodwill.

---

## 5. Trust & safety subgraph

```
Any user content ──[≤2 taps]──► MODR_001 Report ──► reason ──► detail ──► submit
                                                                   │
                                                                   ▼
                                                          confirmation + case ID
                                                                   │
                                                                   └─► SUPP_002 Ticket list (trackable)

Enforcement occurs ──► push notification ──► MODR_002 My notices
                                               │  (rule violated · evidence window · duration)
                                               └─► MODR_003 Appeal ──► case ID ──► SUPP_002
```

`MODR_002` is reachable from Settings at any time, not only from a push. A user who missed the notification must still be able to find out why they were restricted. This is **BT-09** and it is the single largest trust gap in the observed competitor.

---

## 6. State preservation contract

| Route | Survives navigation | Survives process death | Mechanism |
|---|---|---|---|
| `HOME_001` feed position + cursor + tab | ✅ required | ✅ required | saved-state handle + disk cache |
| `DISC_001/003` filters + query + scroll | ✅ | ✅ | saved-state |
| `ROOM_001` chat scrollback | ✅ within session | ➖ | in-memory ring buffer |
| `ROOM_006` gift selection during top-up | ✅ required | ➖ | sheet state hoisted above nav |
| `CHAT_002` draft message | ✅ | ✅ | local DB |
| `CHAT_001` scroll position | ✅ | ✅ | saved-state |
| `PVID_001` session | ✅ | ✅ **must reconnect** | server session survives client restart |
| `WALL_001` pending order | ✅ | ✅ | server-side order state |
| `ROOM_003` pre-live draft | ✅ | ✅ | local DB |

The `PVID_001` row is the sharp one: if a user's app is killed mid-call, the *server session* remains authoritative and either reconnects on relaunch or settles cleanly. Billing must never depend on the client surviving.

---

## 7. Deep links

| Pattern | Destination | Unauthenticated behavior |
|---|---|---|
| `lumena://room/{id}` | `ROOM_001` | guest-viewable |
| `lumena://user/{id}` | `PROF_001` | guest-viewable |
| `lumena://chat/{id}` | `CHAT_002` | → auth, then resume |
| `lumena://wallet` | `WALL_001` | → auth |
| `lumena://wallet/order/{id}` | `WALL_003` | → auth |
| `lumena://call/{id}` | `PVID_002` | → auth, then resume if still ringing |
| `lumena://notice/{id}` | `MODR_002` | → auth |
| `lumena://support/ticket/{id}` | `SUPP_002` | → auth |

Every deep link resolves through the same gate evaluation as in-app navigation. A deep link is not a bypass.

---

## 8. Guest mode

| Surface | Guest |
|---|---|
| Home / Hot / Explore / Search | ✅ browse |
| Live room | ✅ watch |
| Room chat | ❌ → auth prompt |
| Like | ❌ → auth prompt |
| Follow · Gift · Private chat · Private video · Match · Go Live | ❌ → auth prompt |
| Profile view | ✅ read-only |

The auth prompt is a bottom sheet that preserves the pending intent — signing in from "send a gift" returns you to the gift drawer, not to Home.

---

## 9. Back-navigation invariants

1. Back from a destination never loses list position.
2. Back from an overlay dismisses the overlay only.
3. Back from a room returns to the originating feed, not a default tab.
4. Back never re-triggers a network fetch that the cache can serve.
5. Back during an active call minimizes to a floating pill, never silently ends the call and never silently keeps billing invisible.
6. Hardware back and gesture back behave identically.
7. Tab reselection scrolls to top; double-tap refreshes. Neither clears the cursor unless refreshed.
