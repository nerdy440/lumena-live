-- Fraud module: device fingerprints and dispute counts. Mirrors fraud.Mem*Repo.

CREATE TABLE IF NOT EXISTS fraud_device_fingerprints (
    account_id   TEXT NOT NULL,
    device_hash  TEXT NOT NULL,
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_hash, account_id)
);

CREATE TABLE IF NOT EXISTS fraud_disputes (
    account_id  TEXT PRIMARY KEY,
    count       INT NOT NULL DEFAULT 0
);
