-- Admin module: staff role grants, audit log, support tickets. Platform
-- health/economy/fraud views are computed live from other modules'
-- closures (see adminsvc's *Func types) and have no tables here.

CREATE SEQUENCE IF NOT EXISTS admin_audit_seq;
CREATE SEQUENCE IF NOT EXISTS admin_ticket_seq;
CREATE SEQUENCE IF NOT EXISTS admin_ticket_msg_seq;

CREATE TABLE IF NOT EXISTS admin_roles (
    account_id  TEXT PRIMARY KEY,
    role        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_audit_log (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT NOT NULL,
    actor_role  TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_admin_audit_created ON admin_audit_log(created_at DESC);

CREATE TABLE IF NOT EXISTS admin_support_tickets (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL,
    subject      TEXT NOT NULL,
    category     TEXT NOT NULL,
    status       TEXT NOT NULL,
    assigned_to  TEXT NOT NULL DEFAULT '',
    sla_deadline TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_admin_tickets_account ON admin_support_tickets(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_admin_tickets_status ON admin_support_tickets(status);

CREATE TABLE IF NOT EXISTS admin_ticket_messages (
    id          TEXT PRIMARY KEY,
    ticket_id   TEXT NOT NULL REFERENCES admin_support_tickets(id),
    author_id   TEXT NOT NULL,
    is_admin    BOOLEAN NOT NULL DEFAULT false,
    body        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_admin_ticket_messages_ticket ON admin_ticket_messages(ticket_id, created_at);
