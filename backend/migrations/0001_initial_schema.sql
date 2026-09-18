-- Migration: 0001_initial_schema.sql
-- This is the single source of truth for the database schema.
-- The application's DDL in doc 06 is authoritative; this file implements it.
-- Run with: psql $DATABASE_URL -f 0001_initial_schema.sql

BEGIN;

-- ─────────────────────────────────────────────
-- Extensions
-- ─────────────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto; -- for gen_random_bytes

-- ─────────────────────────────────────────────
-- Identity
-- ─────────────────────────────────────────────
CREATE TYPE account_status AS ENUM ('active','restricted','suspended','deleted');

CREATE TABLE accounts (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  phone_e164        text UNIQUE,
  email_normalized  citext UNIQUE,
  password_hash     text,
  status            account_status NOT NULL DEFAULT 'active',
  region_code       char(2) NOT NULL DEFAULT 'XX',
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
  method          text,
  provider_ref    text,
  reviewed_by     uuid,
  CONSTRAINT assured_requires_method
    CHECK (status <> 'assured' OR (assured_at IS NOT NULL AND method IS NOT NULL))
);

CREATE TABLE sessions (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id           uuid NOT NULL REFERENCES accounts(id),
  refresh_token_hash   bytea NOT NULL,
  device_id            text NOT NULL,
  device_label         text,
  ip_inet              inet,
  created_at           timestamptz NOT NULL DEFAULT now(),
  last_seen_at         timestamptz NOT NULL DEFAULT now(),
  revoked_at           timestamptz
);
CREATE INDEX sessions_account_active ON sessions (account_id) WHERE revoked_at IS NULL;

-- ─────────────────────────────────────────────
-- Profile
-- ─────────────────────────────────────────────
CREATE TABLE profiles (
  account_id     uuid PRIMARY KEY REFERENCES accounts(id),
  display_name   text NOT NULL,
  handle         citext UNIQUE,
  bio            text,
  avatar_url     text,
  avatar_state   text NOT NULL DEFAULT 'pending',
  languages      text[] NOT NULL DEFAULT '{}',
  interests      text[] NOT NULL DEFAULT '{}',
  region_code    char(2),
  level          int NOT NULL DEFAULT 1,
  is_creator     boolean NOT NULL DEFAULT false,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX profiles_interests ON profiles USING gin (interests);

CREATE TYPE contact_policy AS ENUM ('everyone','mutuals','nobody');

CREATE TABLE privacy_settings (
  account_id        uuid PRIMARY KEY REFERENCES accounts(id),
  who_can_dm        contact_policy NOT NULL DEFAULT 'everyone',
  who_can_call      contact_policy NOT NULL DEFAULT 'mutuals',
  show_presence     boolean NOT NULL DEFAULT true,
  discoverable      boolean NOT NULL DEFAULT true,
  matchable         boolean NOT NULL DEFAULT false
);

-- ─────────────────────────────────────────────
-- Social graph — three independent tables, three independent axes
-- ─────────────────────────────────────────────
CREATE TABLE follows (
  follower_id  uuid NOT NULL REFERENCES accounts(id),
  followee_id  uuid NOT NULL REFERENCES accounts(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (follower_id, followee_id),
  CONSTRAINT no_self_follow CHECK (follower_id <> followee_id)
);
CREATE INDEX follows_followee ON follows (followee_id, created_at DESC);
CREATE INDEX follows_follower ON follows (follower_id, created_at DESC);

CREATE TABLE blocks (
  blocker_id  uuid NOT NULL REFERENCES accounts(id),
  blocked_id  uuid NOT NULL REFERENCES accounts(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (blocker_id, blocked_id),
  CONSTRAINT no_self_block CHECK (blocker_id <> blocked_id)
);
CREATE INDEX blocks_blocked ON blocks (blocked_id);

CREATE TABLE mutes (
  muter_id  uuid NOT NULL REFERENCES accounts(id),
  muted_id  uuid NOT NULL REFERENCES accounts(id),
  PRIMARY KEY (muter_id, muted_id)
);

CREATE TABLE follow_counts (
  account_id       uuid PRIMARY KEY REFERENCES accounts(id),
  follower_count   bigint NOT NULL DEFAULT 0,
  following_count  bigint NOT NULL DEFAULT 0,
  updated_at       timestamptz NOT NULL DEFAULT now()
);

-- ─────────────────────────────────────────────
-- Rooms & streams
-- ─────────────────────────────────────────────
CREATE TYPE room_status AS ENUM ('scheduled','live','paused','ended','terminated');

CREATE TABLE rooms (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
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
CREATE INDEX rooms_live ON rooms (status, region_code) WHERE status = 'live';
CREATE INDEX rooms_host ON rooms (host_id, created_at DESC);

CREATE TABLE stream_sessions (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  room_id          uuid NOT NULL REFERENCES rooms(id),
  ingest_node      text,
  stream_key_hash  bytea NOT NULL,
  started_at       timestamptz,
  ended_at         timestamptz,
  end_reason       text,
  health_summary   jsonb
);

CREATE TABLE room_participants (
  room_id     uuid NOT NULL REFERENCES rooms(id),
  account_id  uuid NOT NULL REFERENCES accounts(id),
  joined_at   timestamptz NOT NULL DEFAULT now(),
  left_at     timestamptz,
  role        text NOT NULL DEFAULT 'viewer',
  PRIMARY KEY (room_id, account_id, joined_at)
);

-- ─────────────────────────────────────────────
-- Chat
-- ─────────────────────────────────────────────
CREATE TABLE room_messages (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  room_id      uuid NOT NULL REFERENCES rooms(id),
  sender_id    uuid NOT NULL REFERENCES accounts(id),
  body         text NOT NULL,
  lang         text,
  seq          bigint NOT NULL,
  moderation   text NOT NULL DEFAULT 'clean',
  created_at   timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);
CREATE UNIQUE INDEX ON room_messages (room_id, seq);

-- Initial partition (current month)
CREATE TABLE room_messages_2026_09 PARTITION OF room_messages
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

CREATE TABLE conversations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  participant_a   uuid NOT NULL REFERENCES accounts(id),
  participant_b   uuid NOT NULL REFERENCES accounts(id),
  state           text NOT NULL DEFAULT 'active',
  last_message_at timestamptz,
  CONSTRAINT ordered_pair CHECK (participant_a < participant_b),
  UNIQUE (participant_a, participant_b)
);

CREATE TABLE direct_messages (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id  uuid NOT NULL REFERENCES conversations(id),
  sender_id        uuid NOT NULL REFERENCES accounts(id),
  client_msg_id    text NOT NULL,
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

CREATE TABLE direct_messages_2026_09 PARTITION OF direct_messages
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- ─────────────────────────────────────────────
-- Match
-- ─────────────────────────────────────────────
CREATE TABLE match_requests (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id    uuid NOT NULL REFERENCES accounts(id),
  preferences   jsonb NOT NULL DEFAULT '{}',
  state         text NOT NULL DEFAULT 'searching',
  created_at    timestamptz NOT NULL DEFAULT now(),
  resolved_at   timestamptz
);
CREATE INDEX match_requests_searching ON match_requests (created_at) WHERE state = 'searching';

CREATE TABLE match_candidates (
  request_id    uuid NOT NULL REFERENCES match_requests(id),
  candidate_id  uuid NOT NULL REFERENCES accounts(id),
  score         numeric(6,3) NOT NULL,
  score_reasons jsonb NOT NULL,  -- INTERNAL ONLY — serializer denylists this column
  offered_at    timestamptz NOT NULL DEFAULT now(),
  decision      text,
  PRIMARY KEY (request_id, candidate_id)
);

CREATE TABLE match_exclusions (
  account_id  uuid NOT NULL REFERENCES accounts(id),
  excluded_id uuid NOT NULL REFERENCES accounts(id),
  reason      text NOT NULL,
  expires_at  timestamptz,
  PRIMARY KEY (account_id, excluded_id)
);

-- ─────────────────────────────────────────────
-- Private video
-- ─────────────────────────────────────────────
CREATE TYPE pv_state AS ENUM
  ('requesting','ringing','authorizing','connecting','active','reconnecting','ending','settled','failed');

CREATE TABLE pv_sessions (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  caller_id           uuid NOT NULL REFERENCES accounts(id),
  callee_id           uuid NOT NULL REFERENCES accounts(id),
  state               pv_state NOT NULL DEFAULT 'requesting',
  rate_coins_per_min  int NOT NULL,
  authorized_coins    bigint NOT NULL DEFAULT 0,
  consumed_coins      bigint NOT NULL DEFAULT 0,
  hold_id             uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  connected_at        timestamptz,
  ended_at            timestamptz,
  end_reason          text,
  settled_at          timestamptz
);

CREATE TABLE pv_active_intervals (
  session_id  uuid NOT NULL REFERENCES pv_sessions(id),
  started_at  timestamptz NOT NULL,
  ended_at    timestamptz,
  PRIMARY KEY (session_id, started_at)
);

-- ─────────────────────────────────────────────
-- Economy — double-entry ledger
-- ─────────────────────────────────────────────
CREATE TABLE ledger_accounts (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id     uuid REFERENCES accounts(id),
  kind         text NOT NULL,
  currency     text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (owner_id, kind, currency)
);

CREATE TABLE ledger_transactions (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind             text NOT NULL,
  idempotency_key  text NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'posted',
  reversal_of      uuid REFERENCES ledger_transactions(id),
  metadata         jsonb NOT NULL DEFAULT '{}',
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
  id              bigserial PRIMARY KEY,
  transaction_id  uuid NOT NULL REFERENCES ledger_transactions(id),
  account_id      uuid NOT NULL REFERENCES ledger_accounts(id),
  amount          bigint NOT NULL,
  currency        text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_entries_account ON ledger_entries (account_id, created_at DESC);
CREATE INDEX ledger_entries_tx ON ledger_entries (transaction_id);

-- Materialized balance (updated in same transaction as entries)
CREATE TABLE wallet_balances (
  ledger_account_id uuid PRIMARY KEY REFERENCES ledger_accounts(id),
  balance           bigint NOT NULL DEFAULT 0,
  version           bigint NOT NULL DEFAULT 0,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT no_negative_balance CHECK (balance >= 0)   -- structural prevention of overdraft
);

CREATE TABLE ledger_holds (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id     uuid NOT NULL REFERENCES ledger_accounts(id),
  amount         bigint NOT NULL,
  consumed       bigint NOT NULL DEFAULT 0,
  state          text NOT NULL DEFAULT 'held',
  expires_at     timestamptz NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);

-- Orders
CREATE TYPE order_status AS ENUM
  ('created','pending_payment','paid','crediting','credited','failed','refunded');

CREATE TABLE orders (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id            uuid NOT NULL REFERENCES accounts(id),
  sku                   text NOT NULL,
  coins                 bigint NOT NULL,
  price_minor           bigint NOT NULL,
  price_currency        char(3) NOT NULL,
  platform              text NOT NULL,
  purchase_token        text,
  token_hash            bytea UNIQUE,      -- replay prevention across accounts
  status                order_status NOT NULL DEFAULT 'created',
  status_reason         text,
  ledger_transaction_id uuid REFERENCES ledger_transactions(id),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now()
);
-- Reconciler work queue index
CREATE INDEX orders_reconciler ON orders (status, updated_at)
  WHERE status IN ('pending_payment','paid','crediting');

-- ─────────────────────────────────────────────
-- Moderation & enforcement
-- ─────────────────────────────────────────────
CREATE TABLE reports (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  reporter_id   uuid NOT NULL REFERENCES accounts(id),
  subject_type  text NOT NULL,
  subject_id    uuid NOT NULL,
  reason_code   text NOT NULL,
  detail        text,
  evidence_ref  text,
  state         text NOT NULL DEFAULT 'open',
  case_id       text NOT NULL UNIQUE,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE moderation_rules (
  id           text PRIMARY KEY,
  title        text NOT NULL,
  description  text NOT NULL,
  severity     int NOT NULL,
  public_url   text NOT NULL
);

-- Seed: initial rule catalogue
INSERT INTO moderation_rules (id, title, description, severity, public_url) VALUES
  ('CS-01', 'Child sexual abuse material', 'Any sexual content involving minors.', 10, 'https://lumena.live/policy#CS-01'),
  ('CS-02', 'Child safety — other', 'Grooming, solicitation, or endangerment of minors.', 9, 'https://lumena.live/policy#CS-02'),
  ('VL-01', 'Graphic violence', 'Gratuitous or real-world graphic violence.', 8, 'https://lumena.live/policy#VL-01'),
  ('SX-01', 'Explicit sexual content', 'Nudity or sexual acts in a public room.', 7, 'https://lumena.live/policy#SX-01'),
  ('HS-01', 'Hate speech', 'Content targeting protected characteristics.', 7, 'https://lumena.live/policy#HS-01'),
  ('FR-01', 'Fraud and scams', 'Deception for financial gain.', 8, 'https://lumena.live/policy#FR-01'),
  ('SP-01', 'Spam', 'Unsolicited repetitive content or bot behavior.', 4, 'https://lumena.live/policy#SP-01'),
  ('HR-01', 'Harassment', 'Targeted sustained hostile behavior.', 6, 'https://lumena.live/policy#HR-01');

CREATE TABLE enforcements (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id     uuid NOT NULL REFERENCES accounts(id),
  rule_id        text NOT NULL REFERENCES moderation_rules(id),   -- NOT NULL: structural no-unexplained-bans
  action         text NOT NULL,
  duration_hours int,
  evidence_ref   text NOT NULL,
  decided_by     text NOT NULL,
  case_id        text NOT NULL UNIQUE,
  appealable     boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz,
  -- Permanent termination requires a human decision — automated systems cannot permanently ban
  CONSTRAINT permanent_requires_human
    CHECK (action <> 'terminate' OR decided_by LIKE 'human:%')
);
CREATE INDEX enforcements_account ON enforcements (account_id, created_at DESC);

CREATE TABLE appeals (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  enforcement_id uuid NOT NULL REFERENCES enforcements(id),
  account_id     uuid NOT NULL REFERENCES accounts(id),
  statement      text,
  state          text NOT NULL DEFAULT 'pending',
  reviewed_by    uuid,
  decided_at     timestamptz,
  case_id        text NOT NULL UNIQUE
);

-- ─────────────────────────────────────────────
-- Risk
-- ─────────────────────────────────────────────
CREATE TABLE risk_profiles (
  account_id     uuid PRIMARY KEY REFERENCES accounts(id),
  risk_score     numeric(5,2) NOT NULL DEFAULT 0,
  risk_reasons   jsonb NOT NULL DEFAULT '[]',
  review_status  text NOT NULL DEFAULT 'none',
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_fingerprints (
  account_id   uuid NOT NULL REFERENCES accounts(id),
  device_hash  bytea NOT NULL,
  first_seen   timestamptz NOT NULL DEFAULT now(),
  last_seen    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, device_hash)
);
CREATE INDEX device_fingerprints_hash ON device_fingerprints (device_hash);

-- ─────────────────────────────────────────────
-- Support
-- ─────────────────────────────────────────────
CREATE TABLE support_tickets (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id   uuid NOT NULL REFERENCES accounts(id),
  subject      text NOT NULL,
  body         text NOT NULL,
  category     text NOT NULL DEFAULT 'general',
  status       text NOT NULL DEFAULT 'open',
  case_id      text NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_tickets_account ON support_tickets (account_id, created_at DESC);

CREATE TABLE support_messages (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id  uuid NOT NULL REFERENCES support_tickets(id),
  sender     text NOT NULL,  -- 'user' | 'support'
  body       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

COMMIT;
