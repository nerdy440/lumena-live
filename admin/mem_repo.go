package admin

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ─── Role grants ────────────────────────────────────────────────────────────
//
// There is no real IdP/SSO integration for staff accounts — this is a
// minimal in-memory grant store, populated only through the dev-only
// escalation (see adminsvc.Service.DevGrantRole's doc comment). Same
// category of gap as moderation's MemModeratorRepo. NEVER ships to
// production.

type MemRoleRepo struct {
	mu    sync.Mutex
	roles map[string]Role
}

func NewMemRoleRepo() *MemRoleRepo {
	return &MemRoleRepo{roles: make(map[string]Role)}
}

func (r *MemRoleRepo) GetRole(_ context.Context, accountID string) (Role, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	role, ok := r.roles[accountID]
	return role, ok, nil
}

func (r *MemRoleRepo) SetRole(_ context.Context, accountID string, role Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roles[accountID] = role
	return nil
}

// ─── Audit log ──────────────────────────────────────────────────────────────

type MemAuditRepo struct {
	mu      sync.Mutex
	seq     int
	entries []AuditEntry
}

func NewMemAuditRepo() *MemAuditRepo {
	return &MemAuditRepo{}
}

func (r *MemAuditRepo) Append(_ context.Context, e AuditEntry) (*AuditEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.ID = fmt.Sprintf("audit-%04d", r.seq)
	r.entries = append(r.entries, e)
	cp := e
	return &cp, nil
}

func (r *MemAuditRepo) List(_ context.Context, limit int) ([]AuditEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AuditEntry, len(r.entries))
	copy(out, r.entries)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ─── Support tickets ──────────────────────────────────────────────────────

type MemTicketRepo struct {
	mu       sync.Mutex
	seq      int
	byID     map[string]*SupportTicket
	byAcct   map[string][]string
	messages map[string][]TicketMessage
}

func NewMemTicketRepo() *MemTicketRepo {
	return &MemTicketRepo{
		byID:     make(map[string]*SupportTicket),
		byAcct:   make(map[string][]string),
		messages: make(map[string][]TicketMessage),
	}
}

func (r *MemTicketRepo) Create(_ context.Context, t SupportTicket, firstMessage TicketMessage) (*SupportTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	t.ID = fmt.Sprintf("ticket-%04d", r.seq)
	r.byID[t.ID] = &t
	r.byAcct[t.AccountID] = append(r.byAcct[t.AccountID], t.ID)
	firstMessage.ID = fmt.Sprintf("msg-%04d", r.seq)
	firstMessage.TicketID = t.ID
	r.messages[t.ID] = []TicketMessage{firstMessage}
	cp := t
	return &cp, nil
}

func (r *MemTicketRepo) Get(_ context.Context, id string) (*SupportTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrTicketNotFound
	}
	cp := *t
	return &cp, nil
}

func (r *MemTicketRepo) ListByAccount(_ context.Context, accountID string) ([]SupportTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.byAcct[accountID]
	out := make([]SupportTicket, 0, len(ids))
	for _, id := range ids {
		out = append(out, *r.byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (r *MemTicketRepo) ListAll(_ context.Context, statusFilter TicketStatus) ([]SupportTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SupportTicket, 0, len(r.byID))
	for _, t := range r.byID {
		if statusFilter != "" && t.Status != statusFilter {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SLADeadline.Before(out[j].SLADeadline) })
	return out, nil
}

func (r *MemTicketRepo) CountOpen(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, t := range r.byID {
		if t.Status != TicketResolved {
			count++
		}
	}
	return count, nil
}

func (r *MemTicketRepo) UpdateStatusAssignee(_ context.Context, id string, status TicketStatus, assignedTo string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return ErrTicketNotFound
	}
	t.Status = status
	if assignedTo != "" {
		t.AssignedTo = assignedTo
	}
	t.UpdatedAt = time.Now()
	return nil
}

func (r *MemTicketRepo) AddMessage(_ context.Context, ticketID string, m TicketMessage) (*TicketMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[ticketID]; !ok {
		return nil, ErrTicketNotFound
	}
	r.seq++
	m.ID = fmt.Sprintf("msg-%04d", r.seq)
	m.TicketID = ticketID
	r.messages[ticketID] = append(r.messages[ticketID], m)
	r.byID[ticketID].UpdatedAt = time.Now()
	cp := m
	return &cp, nil
}

func (r *MemTicketRepo) ListMessages(_ context.Context, ticketID string) ([]TicketMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TicketMessage, len(r.messages[ticketID]))
	copy(out, r.messages[ticketID])
	return out, nil
}
