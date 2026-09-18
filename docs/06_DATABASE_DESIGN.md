# 06 — Database Design

PostgreSQL 16. IDs are ULIDs stored as `bytea(16)` or UUIDv7 — time-sortable, index-friendly. All timestamps `timestamptz`, UTC. Money is `bigint` in minor units; **no floating point touches money anywhere in this system.**

---

## 1. Identity

```sql
CREATE TYPE account_status AS ENUM ('active','restricted','suspended','deleted');

CREATE TABLE accounts (
  id                uuid PRIMARY KEY,
  phone_e164        text UNIQUE,
  email_normalized  citext UNIQUE,
  password_hash     text,                       -- argon2id; null for OAuth-only
  status            account_status NOT NULL DEFAULT 'active',
  region_code       char(2) NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz,
  CONSTRAINT at_least_one_identifier
    CHECK (phone_e164 IS NOT NULL OR email_normalized IS NOT NULL)
);

CREATE TYPE age_status AS ENUM ('undeclared','declared','assured','failed','appealed');

CREATE TABLE account_age (
  account_id      uuid PRIMARY KEY REFERENCES accounts(id),
  declared_dob    date,
  status          age_status NOT NULL DEFAULT 'undeclared',
  assured_at      timestamptz,
  method          text,        -- 'document' | 'estimation' | 'payment_signal'
  provider_ref    text,        -- vendor reference; NEVER the document itself
  reviewed_by     uuid,
  CONSTRAINT assured_requires_method
    CHECK (status <> 'assured' OR (assured_at IS NOT NULL AND method IS NOT NULL))
);
```

`account_age` is a separate table with its own access control. **We store the assurance *outcome*, not the evidence.** Identity documents are handled by the vendor and never land in our primary database — that eliminates the largest single privacy liability in this product class.

```sql
CREATE TABLE sessions (
  id                 uuid PRIMARY KEY,
  account_id         uuid NOT NULL REFERENCES accounts(id),
  refresh_token_hash bytea NOT NULL,
  device_id          text NOT NULL,
  device_label       text,
  ip_inet            inet,
  created_at         timestamptz NOT NULL DEFAULT now(),
  last_seen_at       timestamptz NOT NULL DEFAULT now(),
  revoked_at         timestamptz
);
CREATE INDEX ON sessions (account_id) WHERE revoked_at IS NULL;
```

## 2. Profile

```sql
CREATE TABLE profiles (
  account_id     uuid PRIMARY KEY REFERENCES accounts(id),
  display_name   text NOT NULL,
  handle         citext UNIQUE,
  bio            text,
  avatar_url     text,
  avatar_state   text NOT NULL DEFAULT 'pending',   -- pending|approved|rejected
  languages      text[] NOT NULL DEFAULT '{}',
  interests      text[] NOT NULL DEFAULT '{}',
  region_code    char(2),
  level          int NOT NULL DEFAULT 1,
  is_creator     boolean NOT NULL DEFAULT false,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON profiles USING gin (interests);
```

Avatars are `pending` until moderated. A profile picture is a publish action and gets treated like one.

## 3. Privacy settings

```sql
CREATE TYPE contact_policy AS ENUM ('everyone','mutuals','nobody');

CREATE TABLE privacy_settings (
  account_id        uuid PRIMARY KEY REFERENCES accounts(id),
  who_can_dm        contact_policy NOT NULL DEFAULT 'everyone',
  who_can_call      contact_policy NOT NULL DEFAULT 'mutuals',
  show_presence     boolean NOT NULL DEFAULT true,
  discoverable      boolean NOT NULL DEFAULT true,
  matchable         boolean NOT NULL DEFAULT false   -- opt-in, not opt-out
);
```

Calls default to mutuals-only and Match is opt-in. Defaults are a safety control, and in this category the permissive default is the wrong one.

## 4. Social graph — the C4/C5 fix

```sql
CREATE TABLE follows (
  follower_id  uuid NOT NULL REFERENCES accounts(id),
  followee_id  uuid NOT NULL REFERENCES accounts(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (follower_id, followee_id),
  CONSTRAINT no_self_follow CHECK (follower_id <> followee_id)
);
CREATE INDEX ON follows (followee_id, created_at DESC);
CREATE INDEX ON follows (follower_id, created_at DESC);

CREATE TABLE blocks (
  blocker_id  uuid NOT NULL REFERENCES accounts(id),
  blocked_id  uuid NOT NULL REFERENCES accounts(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (blocker_id, blocked_id),
  CONSTRAINT no_self_block CHECK (blocker_id <> blocked_id)
);

CREATE TABLE mutes (
  muter_id  uuid NOT NULL REFERENCES accounts(id),
  muted_id  uuid NOT NULL REFERENCES accounts(id),
  PRIMARY KEY (muter_id, muted_id)
);
```

**Three separate tables, three separate axes.** This is the structural answer to the observed defect where unfollowing required blocking. Follow, block, and mute are independent; no operation on one implicitly writes another. Unfollow is `DELETE FROM follows` — a plain, first-class inverse.

**Read-your-own-writes (BT-03).** The Following list is served from the primary, not a replica, when the requester is the list owner:

```sql
-- owner reading own following list → primary, no cache
SELECT f.followee_id, p.display_name, p.avatar_url
FROM follows f JOIN profiles p ON p.account_id = f.followee_id
WHERE f.follower_id = $1
ORDER BY f.created_at DESC
LIMIT $2 OFFSET 0;
```

Counts are denormalized and may lag; **membership never does.** The competitor's bug — follow succeeds but the followee doesn't appear in the list — happens when membership itself is served from a lagging projection. We refuse to do that for the one reader who can tell.

```sql
CREATE TABLE follow_counts (
  account_id       uuid PRIMARY KEY REFERENCES accounts(id),
  follower_count   bigint NOT NULL DEFAULT 0,
  following_count  bigint NOT NULL DEFAULT 0,
  updated_at       timestamptz NOT NULL DEFAULT now()
);
```

## 5. Rooms & streams

```sql
CREATE TYPE room_status AS ENUM ('scheduled','live','paused','ended','terminated');

CREATE TABLE rooms (
  id             uuid PRIMARY KEY,
  host_id        uuid NOT NULL REFERENCES accounts(id),
  title          text,
  tags           text[] NOT NULL DEFAULT '{}',
  region_code    char(2),
  language       text,
  status         room_status NOT NULL DEFAULT 'scheduled',
  cover_url      text,
  started_at     timestamptz,
  ended_at       timestamptz,
  peak_viewers   int NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON rooms (status, region_code) WHERE status = 'live';
CREATE INDEX ON rooms (host_id, created_at DESC);

CREATE TABLE stream_sessions (
  id            uuid PRIMARY KEY,
  room_id       uuid NOT NULL REFERENCES rooms(id),
  ingest_node   text,
  stream_key_hash bytea NOT NULL,   -- never store the key itself
  started_at    timestamptz,
  ended_at      timestamptz,
  end_reason    text,
  health_summary jsonb
);

CREATE TABLE room_participants (
  room_id     uuid NOT NULL REFERENCES rooms(id),
  account_id  uuid NOT NULL REFERENCES accounts(id),
  joined_at   timestamptz NOT NULL DEFAULT now(),
  left_at     timestamptz,
  role        text NOT NULL DEFAULT 'viewer',  -- viewer|moderator|cohost
  PRIMARY KEY (room_id, account_id, joined_at)
);
```

Live viewer counts live in Redis; `room_participants` is the durable audit trail (needed for moderation and fraud, not for the counter).

## 6. Chat

```sql
CREATE TABLE room_messages (
  id           uuid PRIMARY KEY,
  room_id      uuid NOT NULL REFERENCES rooms(id),
  sender_id    uuid NOT NULL REFERENCES accounts(id),
  body         text NOT NULL,
  lang         text,
  seq          bigint NOT NULL,
  moderation   text NOT NULL DEFAULT 'clean',  -- clean|filtered|blocked
  created_at   timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);
CREATE UNIQUE INDEX ON room_messages (room_id, seq);

CREATE TABLE conversations (
  id              uuid PRIMARY KEY,
  participant_a   uuid NOT NULL REFERENCES accounts(id),
  participant_b   uuid NOT NULL REFERENCES accounts(id),
  state           text NOT NULL DEFAULT 'active',  -- active|request|blocked
  last_message_at timestamptz,
  CONSTRAINT ordered_pair CHECK (participant_a < participant_b),
  UNIQUE (participant_a, participant_b)
);

CREATE TABLE direct_messages (
  id               uuid PRIMARY KEY,
  conversation_id  uuid NOT NULL REFERENCES conversations(id),
  sender_id        uuid NOT NULL REFERENCES accounts(id),
  client_msg_id    text NOT NULL,          -- client dedupe
  body             text,
  attachment_id    uuid,
  seq              bigint NOT NULL,
  delivered_at     timestamptz,
  read_at          timestamptz,
  deleted_for      uuid[] NOT NULL DEFAULT '{}',
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (conversation_id, client_msg_id)
) PARTITION BY RANGE (created_at);
CREATE UNIQUE INDEX ON direct_messages (conversation_id, seq);
```

`ordered_pair` guarantees one conversation row per pair regardless of who starts it. `UNIQUE (conversation_id, client_msg_id)` makes send idempotent on retry — the fix for duplicate messages on flaky networks.

## 7. Match

```sql
CREATE TABLE match_requests (
  id            uuid PRIMARY KEY,
  account_id    uuid NOT NULL REFERENCES accounts(id),
  preferences   jsonb NOT NULL,
  state         text NOT NULL DEFAULT 'searching', -- searching|matched|cancelled|timeout
  created_at    timestamptz NOT NULL DEFAULT now(),
  resolved_at   timestamptz
);

CREATE TABLE match_candidates (
  request_id    uuid NOT NULL REFERENCES match_requests(id),
  candidate_id  uuid NOT NULL REFERENCES accounts(id),
  score         numeric(6,3) NOT NULL,
  score_reasons jsonb NOT NULL,     -- INTERNAL ONLY, never serialized to client
  offered_at    timestamptz NOT NULL DEFAULT now(),
  decision      text,               -- connect|skip|expired
  PRIMARY KEY (request_id, candidate_id)
);

CREATE TABLE match_exclusions (
  account_id  uuid NOT NULL REFERENCES accounts(id),
  excluded_id uuid NOT NULL REFERENCES accounts(id),
  reason      text NOT NULL,        -- blocked|reported|skipped_recent|safety
  expires_at  timestamptz,
  PRIMARY KEY (account_id, excluded_id)
);
```

`score_reasons` is deliberately marked internal. Returning "matched because: age 22, female, Pakistan" to a client hands a harasser a targeting tool and leaks sensitive attributes. The API serializer for `match_candidates` has an explicit deny-list including this column, and there is a test asserting it.

## 8. Private video

```sql
CREATE TYPE pv_state AS ENUM
  ('requesting','ringing','authorizing','connecting','active','reconnecting','ending','settled','failed');

CREATE TABLE pv_sessions (
  id                  uuid PRIMARY KEY,
  caller_id           uuid NOT NULL REFERENCES accounts(id),
  callee_id           uuid NOT NULL REFERENCES accounts(id),
  state               pv_state NOT NULL DEFAULT 'requesting',
  rate_coins_per_min  int NOT NULL,
  authorized_coins    bigint NOT NULL DEFAULT 0,
  consumed_coins      bigint NOT NULL DEFAULT 0,
  hold_id             uuid,                  -- FK into ledger holds
  created_at          timestamptz NOT NULL DEFAULT now(),
  connected_at        timestamptz,
  ended_at            timestamptz,
  end_reason          text,
  settled_at          timestamptz
);

-- server-observed billable intervals; settlement derives ONLY from these
CREATE TABLE pv_active_intervals (
  session_id  uuid NOT NULL REFERENCES pv_sessions(id),
  started_at  timestamptz NOT NULL,
  ended_at    timestamptz,
  PRIMARY KEY (session_id, started_at)
);
```

`pv_active_intervals` is why the reconnect-doesn't-bill rule is enforceable rather than aspirational: billing sums server-observed active windows. A client claiming a duration has no effect on the invoice.

## 9. Economy — double-entry ledger

```sql
CREATE TABLE ledger_accounts (
  id           uuid PRIMARY KEY,
  owner_id     uuid REFERENCES accounts(id),   -- null for platform accounts
  kind         text NOT NULL,   -- user_coins | creator_diamonds | platform_revenue
                                -- | platform_liability | payment_clearing | promo_issuance
  currency     text NOT NULL,   -- 'COIN' | 'DIAMOND'
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (owner_id, kind, currency)
);

CREATE TABLE ledger_transactions (
  id               uuid PRIMARY KEY,
  kind             text NOT NULL,   -- purchase|gift|pv_hold|pv_settle|refund|payout|promo|adjustment
  idempotency_key  text NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'posted',  -- posted|reversed
  reversal_of      uuid REFERENCES ledger_transactions(id),
  metadata         jsonb NOT NULL DEFAULT '{}',
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
  id              bigserial PRIMARY KEY,
  transaction_id  uuid NOT NULL REFERENCES ledger_transactions(id),
  account_id      uuid NOT NULL REFERENCES ledger_accounts(id),
  amount          bigint NOT NULL,   -- signed; debit negative, credit positive
  currency        text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON ledger_entries (account_id, created_at DESC);
CREATE INDEX ON ledger_entries (transaction_id);
```

**Invariants, enforced not assumed:**
1. Entries are append-only. No `UPDATE`, no `DELETE`. Revoked at the role level.
2. Every transaction's entries sum to zero per currency — checked in-transaction before commit and re-checked nightly across the whole ledger.
3. Balance is `SUM(amount)`, cached in a materialized row updated in the same transaction. **Never stored independently.**
4. Corrections are reversal transactions, never edits.
5. `idempotency_key` is `UNIQUE`. A duplicate insert fails and the original result is returned. Double-spend is prevented by a database constraint, not by application care.

```sql
CREATE TABLE wallet_balances (
  ledger_account_id uuid PRIMARY KEY REFERENCES ledger_accounts(id),
  balance           bigint NOT NULL DEFAULT 0,
  version           bigint NOT NULL DEFAULT 0,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT no_negative CHECK (balance >= 0)
);
```

`CHECK (balance >= 0)` means a negative balance is not a bug we detect in reporting — it is a transaction that cannot commit.

```sql
CREATE TABLE ledger_holds (        -- pre-authorization for per-minute billing
  id             uuid PRIMARY KEY,
  account_id     uuid NOT NULL REFERENCES ledger_accounts(id),
  amount         bigint NOT NULL,
  consumed       bigint NOT NULL DEFAULT 0,
  state          text NOT NULL DEFAULT 'held',  -- held|settled|released
  expires_at     timestamptz NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);
```

## 10. Orders (purchase) — the C6/C7 fix

```sql
CREATE TYPE order_status AS ENUM
  ('created','pending_payment','paid','crediting','credited','failed','refunded');

CREATE TABLE orders (
  id                 uuid PRIMARY KEY,
  account_id         uuid NOT NULL REFERENCES accounts(id),
  sku                text NOT NULL,
  coins              bigint NOT NULL,
  price_minor        bigint NOT NULL,
  price_currency     char(3) NOT NULL,
  platform           text NOT NULL,   -- google_play | app_store
  purchase_token     text,
  token_hash         bytea UNIQUE,    -- prevents token replay across accounts
  status             order_status NOT NULL DEFAULT 'created',
  status_reason      text,
  ledger_transaction_id uuid REFERENCES ledger_transactions(id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON orders (status, updated_at)
  WHERE status IN ('pending_payment','paid','crediting');
```

That partial index is the reconciler's work queue. Any order stuck in a non-terminal state is picked up automatically — the mechanism that makes "I paid and got nothing" self-healing rather than a support ticket.

## 11. Moderation & enforcement — the BT-09 fix

```sql
CREATE TABLE reports (
  id            uuid PRIMARY KEY,
  reporter_id   uuid NOT NULL REFERENCES accounts(id),
  subject_type  text NOT NULL,   -- user|room|message|pv_session
  subject_id    uuid NOT NULL,
  reason_code   text NOT NULL,
  detail        text,
  evidence_ref  text,
  state         text NOT NULL DEFAULT 'open',   -- open|triaged|actioned|dismissed
  case_id       text NOT NULL UNIQUE,           -- user-facing
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE moderation_rules (
  id           text PRIMARY KEY,        -- e.g. 'CS-01'
  title        text NOT NULL,
  description  text NOT NULL,
  severity     int NOT NULL,
  public_url   text NOT NULL            -- published policy page
);

CREATE TABLE enforcements (
  id             uuid PRIMARY KEY,
  account_id     uuid NOT NULL REFERENCES accounts(id),
  rule_id        text NOT NULL REFERENCES moderation_rules(id),   -- NOT NULL: no unexplained bans
  action         text NOT NULL,   -- warn|mute|restrict_broadcast|restrict_pv|suspend|terminate
  duration_hours int,
  evidence_ref   text NOT NULL,
  decided_by     text NOT NULL,   -- 'auto:<model>' | 'human:<mod_id>'
  case_id        text NOT NULL UNIQUE,
  appealable     boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz,
  CONSTRAINT permanent_requires_human
    CHECK (action <> 'terminate' OR decided_by LIKE 'human:%')
);

CREATE TABLE appeals (
  id             uuid PRIMARY KEY,
  enforcement_id uuid NOT NULL REFERENCES enforcements(id),
  account_id     uuid NOT NULL REFERENCES accounts(id),
  statement      text,
  state          text NOT NULL DEFAULT 'pending',  -- pending|upheld|overturned
  reviewed_by    uuid,
  decided_at     timestamptz,
  case_id        text NOT NULL UNIQUE
);
```

Two constraints carry the whole trust story:
- `rule_id NOT NULL` — **it is physically impossible to record an enforcement without naming the rule.** The competitor's "banned with no reason" complaint cannot be reproduced here, because the schema won't hold that row.
- `permanent_requires_human` — no model permanently terminates an account by itself.

## 12. Risk

```sql
CREATE TABLE risk_profiles (
  account_id     uuid PRIMARY KEY REFERENCES accounts(id),
  risk_score     numeric(5,2) NOT NULL DEFAULT 0,
  risk_reasons   jsonb NOT NULL DEFAULT '[]',
  review_status  text NOT NULL DEFAULT 'none',  -- none|watch|manual_review|restricted
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_fingerprints (
  account_id   uuid NOT NULL REFERENCES accounts(id),
  device_hash  bytea NOT NULL,
  first_seen   timestamptz NOT NULL DEFAULT now(),
  last_seen    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, device_hash)
);
CREATE INDEX ON device_fingerprints (device_hash);   -- multi-account clustering
```

## 13. Retention & deletion

| Data | Retention | On account deletion |
|---|---|---|
| Profile, avatar | Life of account | Deleted |
| Messages | 12 months rolling | Sender copy deleted; recipient copy tombstoned |
| Room messages | 90 days | Pseudonymized |
| **Ledger** | **7 years (financial/legal)** | **Retained, pseudonymized** — cannot be deleted |
| Reports / enforcements | 3 years | Retained, pseudonymized |
| CSAM evidence | Per legal obligation | Retained and reported; deletion request does not apply |
| Analytics | 25 months | Pseudonymized |
| Device fingerprints | 18 months | Retained for ban evasion detection |

**Deletion is not uniform, and the exceptions are legal ones.** The account-deletion flow tells the user exactly this, in plain language, before they confirm — rather than promising total erasure and quietly not delivering it.

## 14. Indexing & partitioning

- `room_messages` and `direct_messages` partitioned monthly; old partitions detached to cold storage.
- `ledger_entries` partitioned monthly, never detached.
- Partial indexes on hot predicates (`status='live'`, unrevoked sessions, non-terminal orders).
- No sharding at launch. Trigger to revisit: primary write utilization sustained >60% or `ledger_entries` >2B rows.
