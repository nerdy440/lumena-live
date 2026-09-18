// Package pgrepo implements adminsvc's RoleRepo, AuditRepo, and TicketRepo
// against real Postgres — the production counterpart to admin.Mem*Repo.
package pgrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/admin"
)

// ─── Roles ──────────────────────────────────────────────────────────────────

type RoleRepo struct{ pool *pgxpool.Pool }

func NewRoleRepo(pool *pgxpool.Pool) *RoleRepo { return &RoleRepo{pool: pool} }

func (r *RoleRepo) GetRole(ctx context.Context, accountID string) (admin.Role, bool, error) {
	var role string
	err := r.pool.QueryRow(ctx, `SELECT role FROM admin_roles WHERE account_id = $1`, accountID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return admin.Role(role), true, nil
}

func (r *RoleRepo) SetRole(ctx context.Context, accountID string, role admin.Role) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO admin_roles (account_id, role) VALUES ($1, $2)
		ON CONFLICT (account_id) DO UPDATE SET role = $2`, accountID, string(role))
	return err
}

// ─── Audit log ──────────────────────────────────────────────────────────────

type AuditRepo struct{ pool *pgxpool.Pool }

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo { return &AuditRepo{pool: pool} }

func (r *AuditRepo) Append(ctx context.Context, e admin.AuditEntry) (*admin.AuditEntry, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('admin_audit_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	e.ID = fmt.Sprintf("audit-%04d", seq)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO admin_audit_log (id, actor_id, actor_role, action, target_type, target_id, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())`,
		e.ID, e.ActorID, string(e.ActorRole), e.Action, e.TargetType, e.TargetID, e.Detail)
	if err != nil {
		return nil, err
	}
	return r.get(ctx, e.ID)
}

func (r *AuditRepo) get(ctx context.Context, id string) (*admin.AuditEntry, error) {
	var e admin.AuditEntry
	var role string
	err := r.pool.QueryRow(ctx, `
		SELECT id, actor_id, actor_role, action, target_type, target_id, detail, created_at
		FROM admin_audit_log WHERE id = $1`, id).
		Scan(&e.ID, &e.ActorID, &role, &e.Action, &e.TargetType, &e.TargetID, &e.Detail, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	e.ActorRole = admin.Role(role)
	return &e, nil
}

func (r *AuditRepo) List(ctx context.Context, limit int) ([]admin.AuditEntry, error) {
	if limit <= 0 {
		limit = 1000000
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, actor_id, actor_role, action, target_type, target_id, detail, created_at
		FROM admin_audit_log ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]admin.AuditEntry, 0)
	for rows.Next() {
		var e admin.AuditEntry
		var role string
		if err := rows.Scan(&e.ID, &e.ActorID, &role, &e.Action, &e.TargetType, &e.TargetID, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ActorRole = admin.Role(role)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─── Support tickets ──────────────────────────────────────────────────────

type TicketRepo struct{ pool *pgxpool.Pool }

func NewTicketRepo(pool *pgxpool.Pool) *TicketRepo { return &TicketRepo{pool: pool} }

const ticketCols = "id, account_id, subject, category, status, assigned_to, sla_deadline, created_at, updated_at"

func scanTicket(row pgx.Row) (*admin.SupportTicket, error) {
	var t admin.SupportTicket
	var status string
	err := row.Scan(&t.ID, &t.AccountID, &t.Subject, &t.Category, &status, &t.AssignedTo, &t.SLADeadline, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, admin.ErrTicketNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Status = admin.TicketStatus(status)
	return &t, nil
}

func (r *TicketRepo) Create(ctx context.Context, t admin.SupportTicket, firstMessage admin.TicketMessage) (*admin.SupportTicket, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('admin_ticket_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	t.ID = fmt.Sprintf("ticket-%04d", seq)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO admin_support_tickets (id, account_id, subject, category, status, sla_deadline, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())
		RETURNING `+ticketCols,
		t.ID, t.AccountID, t.Subject, t.Category, string(t.Status), t.SLADeadline)
	created, err := scanTicket(row)
	if err != nil {
		return nil, err
	}

	var msgSeq int64
	if err := tx.QueryRow(ctx, `SELECT nextval('admin_ticket_msg_seq')`).Scan(&msgSeq); err != nil {
		return nil, err
	}
	firstMessage.ID = fmt.Sprintf("msg-%04d", msgSeq)
	firstMessage.TicketID = t.ID
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_ticket_messages (id, ticket_id, author_id, is_admin, body, created_at)
		VALUES ($1, $2, $3, $4, $5, now())`,
		firstMessage.ID, t.ID, firstMessage.AuthorID, firstMessage.IsAdmin, firstMessage.Body); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func (r *TicketRepo) Get(ctx context.Context, id string) (*admin.SupportTicket, error) {
	return scanTicket(r.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM admin_support_tickets WHERE id = $1`, id))
}

func (r *TicketRepo) ListByAccount(ctx context.Context, accountID string) ([]admin.SupportTicket, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ticketCols+` FROM admin_support_tickets WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]admin.SupportTicket, 0)
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *TicketRepo) ListAll(ctx context.Context, statusFilter admin.TicketStatus) ([]admin.SupportTicket, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+ticketCols+` FROM admin_support_tickets
		WHERE $1 = '' OR status = $1
		ORDER BY sla_deadline ASC`, string(statusFilter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]admin.SupportTicket, 0)
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *TicketRepo) CountOpen(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_support_tickets WHERE status <> 'resolved'`).Scan(&n)
	return n, err
}

func (r *TicketRepo) UpdateStatusAssignee(ctx context.Context, id string, status admin.TicketStatus, assignedTo string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE admin_support_tickets SET
			status = $2,
			assigned_to = CASE WHEN $3 <> '' THEN $3 ELSE assigned_to END,
			updated_at = now()
		WHERE id = $1`, id, string(status), assignedTo)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrTicketNotFound
	}
	return nil
}

func (r *TicketRepo) AddMessage(ctx context.Context, ticketID string, m admin.TicketMessage) (*admin.TicketMessage, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_support_tickets WHERE id = $1)`, ticketID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, admin.ErrTicketNotFound
	}
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('admin_ticket_msg_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	m.ID = fmt.Sprintf("msg-%04d", seq)
	m.TicketID = ticketID
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO admin_ticket_messages (id, ticket_id, author_id, is_admin, body, created_at)
		VALUES ($1, $2, $3, $4, $5, now())`,
		m.ID, ticketID, m.AuthorID, m.IsAdmin, m.Body); err != nil {
		return nil, err
	}
	if _, err := r.pool.Exec(ctx, `UPDATE admin_support_tickets SET updated_at = now() WHERE id = $1`, ticketID); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *TicketRepo) ListMessages(ctx context.Context, ticketID string) ([]admin.TicketMessage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, ticket_id, author_id, is_admin, body, created_at
		FROM admin_ticket_messages WHERE ticket_id = $1 ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]admin.TicketMessage, 0)
	for rows.Next() {
		var m admin.TicketMessage
		if err := rows.Scan(&m.ID, &m.TicketID, &m.AuthorID, &m.IsAdmin, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
