-- Analytics module: account-activity event log (registration/login only —
-- gift/broadcast activity is read live from ledger/streaming instead of
-- duplicated here, see analytics.go's doc comment).

CREATE SEQUENCE IF NOT EXISTS analytics_event_seq;

CREATE TABLE IF NOT EXISTS analytics_events (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_analytics_events_created ON analytics_events(created_at);
CREATE INDEX IF NOT EXISTS idx_analytics_events_type ON analytics_events(type);
