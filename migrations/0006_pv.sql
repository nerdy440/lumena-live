-- Private video call module. Mirrors pv.MemRepo / pv.Session.

CREATE TABLE IF NOT EXISTS pv_sessions (
    id                   TEXT PRIMARY KEY,
    caller_id            TEXT NOT NULL,
    callee_id            TEXT NOT NULL,
    state                TEXT NOT NULL,
    rate_per_min_coins   BIGINT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at          TIMESTAMPTZ,
    ended_at             TIMESTAMPTZ,
    consumed_coins       BIGINT NOT NULL DEFAULT 0,
    hold_transaction_id  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_pv_sessions_caller ON pv_sessions(caller_id);
CREATE INDEX IF NOT EXISTS idx_pv_sessions_callee ON pv_sessions(callee_id);
