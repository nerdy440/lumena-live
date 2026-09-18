-- Profile module: public-facing profile data, counts, privacy settings,
-- and the social graph (follows/blocks/mutes). Mirrors profile.Profile,
-- profile.PrivacySettings, and social.MemSocialRepo's three tables.

CREATE TABLE IF NOT EXISTS profiles (
    account_id      TEXT PRIMARY KEY,
    display_name    TEXT NOT NULL,
    handle          TEXT UNIQUE,
    bio             TEXT,
    avatar_url      TEXT,
    avatar_state    TEXT NOT NULL DEFAULT 'none',
    languages       TEXT[] NOT NULL DEFAULT '{}',
    interests       TEXT[] NOT NULL DEFAULT '{}',
    region_code     TEXT NOT NULL DEFAULT '',
    level           INT NOT NULL DEFAULT 1,
    is_creator      BOOLEAN NOT NULL DEFAULT false,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS privacy_settings (
    account_id      TEXT PRIMARY KEY REFERENCES profiles(account_id) ON DELETE CASCADE,
    who_can_dm      TEXT NOT NULL DEFAULT 'everyone',
    who_can_call    TEXT NOT NULL DEFAULT 'everyone',
    show_presence   BOOLEAN NOT NULL DEFAULT true,
    discoverable    BOOLEAN NOT NULL DEFAULT true,
    matchable       BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS follows (
    follower_id     TEXT NOT NULL,
    followee_id     TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (follower_id, followee_id)
);
CREATE INDEX IF NOT EXISTS idx_follows_followee ON follows(followee_id);

CREATE TABLE IF NOT EXISTS blocks (
    blocker_id      TEXT NOT NULL,
    blocked_id      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id)
);

CREATE TABLE IF NOT EXISTS mutes (
    muter_id        TEXT NOT NULL,
    muted_id        TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (muter_id, muted_id)
);
