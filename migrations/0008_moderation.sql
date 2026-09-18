-- Moderation module: reports, enforcements, appeals, risk profiles,
-- moderator role set. Mirrors moderation.Mem*Repo.

CREATE SEQUENCE IF NOT EXISTS moderation_report_seq;
CREATE SEQUENCE IF NOT EXISTS moderation_enforcement_seq;
CREATE SEQUENCE IF NOT EXISTS moderation_appeal_seq;

CREATE TABLE IF NOT EXISTS moderation_reports (
    id            TEXT PRIMARY KEY,
    reporter_id   TEXT NOT NULL,
    subject_type  TEXT NOT NULL,
    subject_id    TEXT NOT NULL,
    reason_code   TEXT NOT NULL,
    detail        TEXT NOT NULL DEFAULT '',
    evidence_ref  TEXT NOT NULL DEFAULT '',
    state         TEXT NOT NULL,
    case_id       TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mod_reports_reporter_subject ON moderation_reports(reporter_id, subject_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mod_reports_state ON moderation_reports(state);

CREATE TABLE IF NOT EXISTS moderation_enforcements (
    id              TEXT PRIMARY KEY,
    account_id      TEXT NOT NULL,
    rule_id         TEXT NOT NULL,
    action          TEXT NOT NULL,
    duration_hours  INT NOT NULL DEFAULT 0,
    evidence_ref    TEXT NOT NULL DEFAULT '',
    decided_by      TEXT NOT NULL DEFAULT '',
    case_id         TEXT NOT NULL,
    appealable      BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_mod_enforcements_account ON moderation_enforcements(account_id, created_at DESC);

CREATE TABLE IF NOT EXISTS moderation_appeals (
    id               TEXT PRIMARY KEY,
    enforcement_id   TEXT NOT NULL,
    account_id       TEXT NOT NULL,
    statement        TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL,
    reviewed_by      TEXT NOT NULL DEFAULT '',
    decided_at       TIMESTAMPTZ,
    case_id          TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One pending appeal per enforcement at a time — matches MemAppealRepo's
-- byEnforcement map (last-write-wins there; here we enforce it with a
-- partial unique index instead so a second concurrent pending appeal for
-- the same enforcement is rejected outright, not silently overwritten).
CREATE UNIQUE INDEX IF NOT EXISTS idx_mod_appeals_one_pending_per_enforcement
    ON moderation_appeals(enforcement_id) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS moderation_risk_profiles (
    account_id     TEXT PRIMARY KEY,
    risk_score     DOUBLE PRECISION NOT NULL DEFAULT 0,
    risk_reasons   TEXT[] NOT NULL DEFAULT '{}',
    review_status  TEXT NOT NULL DEFAULT 'none',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS moderation_moderators (
    account_id  TEXT PRIMARY KEY
);
