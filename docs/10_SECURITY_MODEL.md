# 10 — Security Model

This document covers security and trust & safety together because in a product with 1-to-1 stranger video and a real-money economy, they are not separate topics.

---

## 1. Hard gates — prerequisites to launch

The following are not features. They are prerequisites. The product does not ship the affected surfaces without them.

| Gate | Blocks | Status |
|---|---|---|
| Age declaration for all accounts | Broadcast + private video + match | NOT IMPLEMENTED |
| Age assurance for broadcast and private video | Broadcast + private video | NOT IMPLEMENTED |
| Real-time visual moderation (live streams) | Broadcast | NOT IMPLEMENTED |
| CSAM hash-matching + NCMEC reporting pipeline | Broadcast + private video | NOT IMPLEMENTED |
| Grooming-pattern detection (adult→minor signals) | Chat + private video | NOT IMPLEMENTED |
| Spend limits + cooling-off (player protection) | All money features | NOT IMPLEMENTED |

---

## 2. Child safety — CSAM and age assurance

This section is longer than other security sections because the failure mode is not financial or reputational — it is direct harm to children.

### 2a. Age declaration

Every account declares a date of birth at registration. Accounts declaring an age below the minimum for the jurisdiction are hard-blocked — the account is created but every gated surface returns `UNDER_AGE_MINIMUM`. The DOB is not stored in a way that is easily correctable by the user after the fact (it can be changed once via a support ticket that requires ID verification).

The minimum age is 18 for broadcast and private video. 17 in some jurisdictions for general viewing, where local law requires — enforced by region code. When in doubt, 18.

### 2b. Age assurance before broadcast and private video

Declaration is self-reported and unverified. For the two highest-risk surfaces (broadcast and 1-to-1 private video), a second check is required:

**Available methods (vendor-pluggable interface):**

```go
type AgeAssuranceProvider interface {
    StartVerification(ctx context.Context, req AgeAssuranceRequest) (*AssuranceSession, error)
    GetStatus(ctx context.Context, sessionID string) (*AssuranceResult, error)
    WebhookHandler() http.Handler
}

type AgeAssuranceRequest struct {
    AccountID   string
    Method      string // "document" | "payment_signal" | "estimation"
    RedirectURL string
}

type AssuranceResult struct {
    Status     string // "assured" | "failed" | "pending"
    Method     string
    ProviderRef string // vendor reference only — no document content stored
    AssuredAt  *time.Time
}
```

We store the **outcome** (assured/failed/pending) and the **vendor reference** for audit. We do not store the document, the image, or any biometric. The document never transits our primary infrastructure — it is handled by the vendor and the vendor confirms the outcome.

If a vendor is not yet integrated, the gate returns `AGE_ASSURANCE_REQUIRED` and the surface is unavailable. The gate is not bypassed.

### 2c. CSAM detection pipeline

This is a legal obligation (18 U.S.C. § 2258A in the US; equivalent in other jurisdictions) and not a product decision. The pipeline:

1. **Hash matching (PhotoDNA-compatible):** every image uploaded (avatar, attachment, gift) and every frame sample from live streams and private video sessions is hashed. Hashes are matched against NCMEC's hash database via the CyberTipline API.
2. **On match:** immediate surface termination (stream killed, session ended, message blocked). Content locked. Automatic `CyberTipline` report generated and submitted. Human review queue flagged. Account restricted pending review.
3. **Evidence retention:** per legal obligation. Not subject to user deletion requests.
4. **False-positive handling:** the hash match rate on a clean platform is effectively zero (these are perceptual hashes of known CSAM). A match is treated as a confirmed signal requiring reporting, not a score requiring further evaluation.

For **private video sessions where P2P is established** (server does not relay the media):
- The mobile client runs a local on-device hash-matching model.
- On match: client immediately terminates the session via `POST /private-video/sessions/{id}/end?reason=safety` and submits a hash report to the server, which then files the CyberTipline report.
- The local model is updated via a signed OTA bundle, not through an app store update.
- This approach is documented in the privacy policy. Users have no opt-out from safety scanning.

### 2d. Grooming-pattern detection

Signals monitored (no single signal triggers action; scored together):
- Adult account contacting multiple recently-registered low-follower accounts
- Conversation moving rapidly from room chat to private chat to private video request
- Requests for private video toward accounts that have not demonstrated reciprocal social engagement
- Language patterns (keyword signals; not full content scanning for general messages)
- Age-gap signals where age assurance data permits inference

Output: `risk_reasons` entry, elevated `risk_score`, optional manual-review flag. Not an automated ban.

---

## 3. Authentication & token security

- Access tokens: JWT, RS256, 15-minute expiry, audience-bound.
- Refresh tokens: opaque random 256-bit, stored as Argon2id hash, bound to `(account_id, device_id)`. **Refresh token rotation with reuse detection** (RFC 6749 §10.4): receiving a revoked refresh token revokes the entire family for that device.
- WebSocket tokens: 60-second single-use, exchanged via an authenticated REST call immediately before connecting.
- Session revocation propagates to the realtime gateway within 30 seconds via Kafka.
- Passwords: Argon2id (m=65536, t=3, p=4). Pepper in HSM.
- No password in logs, headers, or URLs anywhere in the system. Enforced by log-scrubbing middleware and a pre-commit hook.

---

## 4. Authorization model

All authorization is server-side. The client's claims are not trusted.

```
Every request:
1. Verify token signature and expiry
2. Load account status → reject if suspended/deleted
3. Load account restrictions → apply per-surface checks (broadcast_restricted, pv_restricted, ...)
4. Load age_status → check surface minimum
5. Check block relation where relevant
6. Check privacy settings where relevant
7. Enforce idempotency
8. Then execute
```

**Row-level security** is enforced in the database for the ledger service (separate DB role with restricted grants) and the moderation evidence store. Application-level checks are defense-in-depth, not the primary control.

**Private video gate** (server-side, called in this exact order, first failure wins):
1. Caller active + no restrictions
2. Callee active + no restrictions
3. Caller age assured
4. Callee age assured (check only — result not disclosed to caller)
5. No block in either direction
6. Callee privacy permits this caller's contact class
7. Sufficient pre-authorized balance

Every gate must pass. The failure code returned to the client for gates 2–6 is the same: `RECIPIENT_UNAVAILABLE`. Gate 1 and 7 return specific codes because those are actionable by the caller.

---

## 5. Economy security

The ledger is the security boundary for money. Key controls:

- **`CHECK (balance >= 0)`** at the DB level. A negative balance cannot be committed.
- **`UNIQUE (idempotency_key)`** on transactions. Replay of any gift or purchase is structurally impossible — the insert fails and the original result is returned.
- **`token_hash UNIQUE`** on orders. A purchase token cannot be replayed to credit a second account.
- **Server verifies purchase tokens with the store**, never trusts client-reported amounts. Client cannot claim "I paid for 10,000 coins" — the server confirms with Google/Apple.
- **No floating-point arithmetic on any money path.** Minor currency units (coins) as `bigint` from ingestion to display.
- **Payout hold period.** Creator earnings are subject to a configurable hold (default 14 days) before withdrawal is permitted — the chargeback window.
- **Gift loop detection.** A→B sends gift, B→A sends gift within T minutes: elevated risk signal. Not an automatic ban. Accumulates in `risk_reasons`.
- **Velocity limits (Redis):** per-user: max 10 gifts per minute, max 50 per hour. Per-room-sender: max 30 messages per minute. Per-gift-value: gifts above N coins require a 5-second confirmation dialog (not a limit, a friction point).

---

## 6. Transport & infrastructure

- TLS 1.3 everywhere. TLS 1.2 supported only for legacy broadcaster tools; 1.1 and below rejected.
- HSTS with preloading.
- Certificate transparency monitoring.
- TURN media relay: SRTP (libsrtp), DTLS-SRTP negotiation mandatory.
- API Gateway: WAF (OWASP top 10 rules), request signing for internal service calls.
- Secrets: HSM for encryption keys, secrets manager for API keys. No secrets in environment variables in Kubernetes pods (use mounted secrets or secret store CSI driver).
- Network: private VPC, no public ingress except CDN edge, API gateway, and TURN. All internal service communication over mTLS.

---

## 7. Sensitive attribute handling

Certain attributes are processed by the server for filtering but never returned to clients:

| Attribute | Stored | Used for | Returned to client |
|---|---|---|---|
| Declared DOB | Hashed after assurance | Age-gate check | Never |
| Assurance outcome | Yes (assured/failed) | Gate | `age_status` enum only — no DOB |
| Block list | Yes | Filtering, gate | Self-view only |
| `match_candidates.score_reasons` | Yes | Internal | **Never** |
| Device fingerprints | Hashed | Fraud clustering | Never |
| IP address | Gateway logs, 90d retention | Fraud, abuse | Never |
| Gender preference (match) | Yes | Filtering only | **Never returned in candidate payload** |

Gender filtering in the match system is implemented only in jurisdictions where it is legal, and uses a preference-signal model rather than requiring users to disclose their own gender to the system. The implementation is reviewed by legal counsel per region before enablement.

---

## 8. Moderation transparency (BT-09) — enforced at the schema level

Reiterated here because it is both a safety control and an authorization control.

Every enforcement record in the database has:
- `rule_id NOT NULL` — references a public rule with a stable ID and URL
- `evidence_ref NOT NULL` — references retained evidence
- `decided_by NOT NULL` — distinguishes human from automated
- `case_id UNIQUE` — user-facing reference usable in appeals and support

Permanent termination has an additional constraint: `CHECK (action <> 'terminate' OR decided_by LIKE 'human:%')`. Automated systems can warn, mute, restrict, and suspend. They cannot terminate accounts permanently. This is a database constraint, not a policy.

An account under restriction can always access `GET /me/enforcements` and `POST /me/enforcements/{id}/appeal`, even if they are restricted from every other surface. The restriction cannot lock them out of the transparency and appeal surface.

---

## 9. Penetration testing & security review checklist

Before any surface ships:

- [ ] Auth bypass attempts (token forgery, session fixation, IDOR)
- [ ] Money: replay attack on gift send (same idempotency key, different gift)
- [ ] Money: negative-amount injection attempt
- [ ] Money: purchase-token replay across accounts
- [ ] Money: balance assertion bypass attempt
- [ ] Private video: gate bypass (direct `POST /streams/{id}/start` without age assurance)
- [ ] WebSocket: subscribe to another user's `user:{id}` private topic
- [ ] WebSocket: inject events to a room you are not a participant of
- [ ] IDOR on conversations, messages, sessions, orders, enforcements
- [ ] Mass assignment on profile fields (attempt to set `is_creator`, `age_status`, etc.)
- [ ] Report abuse: mass-report flooding (rate-limited per reporter per subject per 24h)
- [ ] TURN credential theft: use leaked TURN credentials to proxy non-session media

Results documented, mitigations implemented, re-tested before launch.
