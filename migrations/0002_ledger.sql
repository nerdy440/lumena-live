-- Ledger module: append-only double-entry transactions. Mirrors
-- ledger.Transaction / ledger.Entry exactly — see ledger/ledger.go's
-- Repo interface doc comment for the invariants this schema must
-- enforce (idempotent posts, balanced-to-zero entries, never-negative
-- balances, "never destroy financial history" reversals).
--
-- Balances are deliberately NOT stored redundantly — they're derived by
-- summing ledger_entries under a transaction-scoped lock at write time
-- (see ledger/pgrepo's PostTransaction), the same "derive, don't cache"
-- approach the in-memory implementation's own doc comment describes as
-- the real-database plan.

CREATE TABLE IF NOT EXISTS ledger_transactions (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id              BIGSERIAL PRIMARY KEY,
    transaction_id  TEXT NOT NULL REFERENCES ledger_transactions(id),
    account_id      TEXT NOT NULL,
    amount          BIGINT NOT NULL,
    currency        TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ledger_entries_account ON ledger_entries(account_id, currency);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transaction ON ledger_entries(transaction_id);

-- original -> reversal, one-to-one, enforced by both columns being
-- unique — mirrors mem_ledger.go's reversalOf map and its "cannot
-- reverse a reversal" / "cannot reverse twice" checks.
CREATE TABLE IF NOT EXISTS ledger_reversals (
    original_transaction_id  TEXT PRIMARY KEY REFERENCES ledger_transactions(id),
    reversal_transaction_id  TEXT NOT NULL UNIQUE REFERENCES ledger_transactions(id)
);
