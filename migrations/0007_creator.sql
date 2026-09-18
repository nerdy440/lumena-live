-- Creator dashboard module: KYC, payouts, payout profiles. Earnings and
-- stream/follower analytics are computed live from ledger/streaming/social
-- data (see creatorsvc's closures) and have no tables of their own here.

CREATE SEQUENCE IF NOT EXISTS creator_payout_seq;

CREATE TABLE IF NOT EXISTS creator_kyc (
    account_id    TEXT PRIMARY KEY,
    status        TEXT NOT NULL,
    legal_name    TEXT NOT NULL DEFAULT '',
    country       TEXT NOT NULL DEFAULT '',
    submitted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS creator_payouts (
    id                       TEXT PRIMARY KEY,
    account_id               TEXT NOT NULL,
    amount_diamonds          BIGINT NOT NULL,
    status                   TEXT NOT NULL,
    failure_reason           TEXT NOT NULL DEFAULT '',
    transaction_id           TEXT NOT NULL DEFAULT '',
    reversal_transaction_id  TEXT NOT NULL DEFAULT '',
    payout_reference         TEXT NOT NULL DEFAULT '',
    admin_note               TEXT NOT NULL DEFAULT '',
    reviewed_by               TEXT NOT NULL DEFAULT '',
    requested_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at                TIMESTAMPTZ,
    paid_at                    TIMESTAMPTZ,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_creator_payouts_account ON creator_payouts(account_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_creator_payouts_status ON creator_payouts(status);

CREATE TABLE IF NOT EXISTS creator_payout_profiles (
    account_id   TEXT PRIMARY KEY,
    method       TEXT NOT NULL DEFAULT '',
    country      TEXT NOT NULL DEFAULT '',
    payee_name   TEXT NOT NULL DEFAULT '',
    destination  TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
