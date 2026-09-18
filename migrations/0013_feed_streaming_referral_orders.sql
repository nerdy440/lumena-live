-- Remaining Phase 1 modules: feed (rooms/premium-unlocks/notifications),
-- streaming (broadcast sessions), referral, and ledger's orders/spend-limits.

-- ─── Feed ───────────────────────────────────────────────────────────────────

CREATE SEQUENCE IF NOT EXISTS feed_room_seq;

CREATE TABLE IF NOT EXISTS feed_rooms (
    room_id              TEXT PRIMARY KEY,
    host_id              TEXT NOT NULL,
    host_name            TEXT NOT NULL DEFAULT '',
    host_avatar          TEXT,
    host_level           INT NOT NULL DEFAULT 0,
    title                TEXT NOT NULL DEFAULT '',
    cover_url            TEXT,
    tags                 TEXT[] NOT NULL DEFAULT '{}',
    language             TEXT NOT NULL DEFAULT '',
    region_code          TEXT NOT NULL DEFAULT '',
    viewer_count         INT NOT NULL DEFAULT 0,
    peak_viewers         INT NOT NULL DEFAULT 0,
    status               TEXT NOT NULL,
    started_at           TIMESTAMPTZ,
    ended_at             TIMESTAMPTZ,
    gift_velocity        DOUBLE PRECISION NOT NULL DEFAULT 0,
    follower_growth_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    is_premium           BOOLEAN NOT NULL DEFAULT false,
    unlock_price_coins   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_feed_rooms_status ON feed_rooms(status);
CREATE INDEX IF NOT EXISTS idx_feed_rooms_host ON feed_rooms(host_id);

CREATE TABLE IF NOT EXISTS feed_premium_unlocks (
    room_id     TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    PRIMARY KEY (room_id, account_id)
);

CREATE SEQUENCE IF NOT EXISTS feed_notification_seq;
-- account_id here mirrors MemNotificationRepo's actual (slightly odd)
-- behavior faithfully: Create buckets a notification under its own
-- ActorID, and List/MarkRead/GetSummary read back by that same key —
-- see feed/pgrepo's doc comment. Notification.Create has no caller
-- anywhere in this codebase today, so this is dead-but-mirrored code,
-- not a live bug surface.
CREATE TABLE IF NOT EXISTS feed_notifications (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL,
    type        TEXT NOT NULL,
    actor_id    TEXT,
    actor_name  TEXT,
    body        TEXT NOT NULL DEFAULT '',
    read        BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deep_link   TEXT NOT NULL DEFAULT '',
    order_seq   BIGINT NOT NULL DEFAULT nextval('feed_notification_seq')
);
CREATE INDEX IF NOT EXISTS idx_feed_notifications_account ON feed_notifications(account_id, order_seq DESC);

-- ─── Streaming ──────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS streaming_sessions (
    id             TEXT PRIMARY KEY,
    room_id        TEXT NOT NULL,
    host_id        TEXT NOT NULL,
    state          TEXT NOT NULL,
    ingest_node    TEXT NOT NULL DEFAULT '',
    stream_key_id  TEXT NOT NULL DEFAULT '',
    playback_url   TEXT NOT NULL DEFAULT '',
    started_at     TIMESTAMPTZ,
    ended_at       TIMESTAMPTZ,
    end_reason     TEXT NOT NULL DEFAULT '',
    peak_viewers   INT NOT NULL DEFAULT 0,
    -- Health is a point-in-time snapshot pushed every 5s over the WS, not
    -- an append-only series — one JSONB column mirrors MemSessionRepo's
    -- single *StreamHealth pointer exactly (last write wins).
    health         JSONB
);
CREATE INDEX IF NOT EXISTS idx_streaming_sessions_room ON streaming_sessions(room_id);
CREATE INDEX IF NOT EXISTS idx_streaming_sessions_host ON streaming_sessions(host_id);
CREATE INDEX IF NOT EXISTS idx_streaming_sessions_state ON streaming_sessions(state) WHERE state IN ('live', 'reconnecting');

-- ─── Referral ───────────────────────────────────────────────────────────────

CREATE SEQUENCE IF NOT EXISTS referral_seq;

CREATE TABLE IF NOT EXISTS referral_claims (
    account_id  TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS referrals (
    id              TEXT PRIMARY KEY,
    referrer_id     TEXT NOT NULL,
    referred_id     TEXT NOT NULL,
    reward_coins    BIGINT NOT NULL,
    transaction_id  TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_referrals_referrer ON referrals(referrer_id, created_at DESC);

-- ─── Orders / spend limits ────────────────────────────────────────────────

CREATE SEQUENCE IF NOT EXISTS ledger_order_seq;

CREATE TABLE IF NOT EXISTS ledger_orders (
    id                     TEXT PRIMARY KEY,
    account_id             TEXT NOT NULL,
    sku                    TEXT NOT NULL,
    coins                  BIGINT NOT NULL,
    price_minor            BIGINT NOT NULL,
    price_currency         TEXT NOT NULL,
    platform               TEXT NOT NULL DEFAULT '',
    purchase_token         TEXT NOT NULL DEFAULT '',
    token_hash             TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL,
    status_reason          TEXT NOT NULL DEFAULT '',
    ledger_transaction_id  TEXT NOT NULL DEFAULT '',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ledger_orders_account ON ledger_orders(account_id);
-- Only one order may ever hold a given non-empty token hash — the C6/C7
-- replay-prevention invariant (doc 06 §10). A partial unique index (not a
-- plain UNIQUE) so the many orders with token_hash = '' before a token is
-- ever attached don't collide with each other.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_orders_token_hash
    ON ledger_orders(token_hash) WHERE token_hash <> '';
CREATE INDEX IF NOT EXISTS idx_ledger_orders_nonterminal ON ledger_orders(status)
    WHERE status IN ('pending_payment', 'paid', 'crediting');

CREATE TABLE IF NOT EXISTS ledger_spend_limits (
    account_id            TEXT PRIMARY KEY,
    daily_cap             BIGINT NOT NULL DEFAULT 0,
    weekly_cap            BIGINT NOT NULL DEFAULT 0,
    monthly_cap           BIGINT NOT NULL DEFAULT 0,
    cooling_off           BOOLEAN NOT NULL DEFAULT false,
    cooling_off_until     TIMESTAMPTZ,
    pending_daily_cap     BIGINT NOT NULL DEFAULT 0,
    pending_weekly_cap    BIGINT NOT NULL DEFAULT 0,
    pending_monthly_cap   BIGINT NOT NULL DEFAULT 0,
    pending_effective_at  TIMESTAMPTZ
);
