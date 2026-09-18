-- Auth module: accounts, sessions, password hashes, age declarations.
-- Mirrors auth/authsvc.Account, .Session, and the age-assurance fields
-- exactly — see auth/authsvc/service.go for the Go-side contract this
-- schema backs.

CREATE TABLE IF NOT EXISTS accounts (
    id              TEXT PRIMARY KEY,
    phone_e164      TEXT UNIQUE,
    email           TEXT UNIQUE,
    status          TEXT NOT NULL DEFAULT 'active',
    region_code     TEXT NOT NULL DEFAULT '',
    age_status      TEXT NOT NULL DEFAULT 'undeclared',
    date_of_birth   DATE,
    is_new_account  BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS password_hashes (
    account_id      TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    hash            TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    account_id      TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id       TEXT NOT NULL,
    device_label    TEXT NOT NULL DEFAULT '',
    token_hash      TEXT NOT NULL UNIQUE,
    ip              TEXT NOT NULL DEFAULT '',
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sessions_account_device ON sessions(account_id, device_id);
