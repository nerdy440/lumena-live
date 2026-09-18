-- Matchmaking module. Mirrors match.MemRepo.

CREATE TABLE IF NOT EXISTS match_requests (
    id                TEXT PRIMARY KEY,
    account_id        TEXT NOT NULL,
    gender_preference TEXT NOT NULL DEFAULT '',
    min_age           INT NOT NULL DEFAULT 0,
    max_age           INT NOT NULL DEFAULT 0,
    state             TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at       TIMESTAMPTZ,
    matched_with      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_match_requests_account ON match_requests(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_match_requests_searching ON match_requests(state) WHERE state = 'searching';

CREATE TABLE IF NOT EXISTS match_candidates (
    request_id    TEXT NOT NULL REFERENCES match_requests(id),
    candidate_id  TEXT NOT NULL,
    score         DOUBLE PRECISION NOT NULL DEFAULT 0,
    score_reasons JSONB,
    offered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decision      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (request_id, candidate_id)
);

CREATE TABLE IF NOT EXISTS match_preferences (
    account_id        TEXT PRIMARY KEY,
    gender_preference TEXT NOT NULL DEFAULT '',
    min_age           INT NOT NULL DEFAULT 0,
    max_age           INT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS match_exclusions (
    account_id  TEXT NOT NULL,
    excluded_id TEXT NOT NULL,
    reason      TEXT NOT NULL,
    expires_at  TIMESTAMPTZ,
    PRIMARY KEY (account_id, excluded_id)
);
