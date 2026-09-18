-- Rollout module: feature flags and the internal-accounts allowlist.
-- Canary metrics (rollout.MetricsRepo) are deliberately NOT persisted —
-- see rollout.go's doc comment: this process only ever runs one binary,
-- so its in-flight latency/error counters are ephemeral process state,
-- not durable data, and stay on rollout.MemMetricsRepo in every backend.

CREATE TABLE IF NOT EXISTS rollout_flags (
    key         TEXT PRIMARY KEY,
    stage       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rollout_internal_accounts (
    account_id  TEXT PRIMARY KEY
);
