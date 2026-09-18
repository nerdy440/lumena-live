-- Chat module: private 1-to-1 messaging. Mirrors chat.MemChatRepo.
-- order_seq is a single global sequence shared by conversation creation
-- and "touched by a new message" updates, matching MemChatRepo's
-- globalOrder counter — ListConversations sorts by it descending so the
-- most recently active conversation is always first, exactly like the
-- in-memory implementation.

CREATE SEQUENCE IF NOT EXISTS chat_order_seq;

CREATE TABLE IF NOT EXISTS conversations (
    id              TEXT PRIMARY KEY,
    participant_a   TEXT NOT NULL,
    participant_b   TEXT NOT NULL,
    initiated_by    TEXT NOT NULL,
    state           TEXT NOT NULL,
    last_message_at TIMESTAMPTZ,
    order_seq       BIGINT NOT NULL DEFAULT nextval('chat_order_seq'),
    UNIQUE (participant_a, participant_b)
);

CREATE TABLE IF NOT EXISTS direct_messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    sender_id       TEXT NOT NULL,
    client_msg_id   TEXT NOT NULL,
    body            TEXT NOT NULL,
    attachment_id   TEXT NOT NULL DEFAULT '',
    seq             BIGINT NOT NULL,
    delivered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at         TIMESTAMPTZ,
    deleted_for_all BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, client_msg_id),
    UNIQUE (conversation_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_dm_conv_seq ON direct_messages(conversation_id, seq);

-- Per-viewer "delete for me" — never serialized, never visible to anyone
-- but the viewer who deleted it (see chat.Message.DeletedForMe's doc
-- comment: json:"-").
CREATE TABLE IF NOT EXISTS message_deletes (
    message_id  TEXT NOT NULL REFERENCES direct_messages(id),
    account_id  TEXT NOT NULL,
    PRIMARY KEY (message_id, account_id)
);
