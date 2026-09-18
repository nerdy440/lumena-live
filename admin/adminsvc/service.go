// Package adminsvc implements the admin dashboard service (roadmap Phase
// 16): RBAC-gated account management, read-only economy oversight, a fraud
// review queue, support-ticket escalation, and platform health — with
// every mutating action audit-logged. Every dependency on auth/ledger/
// moderation/creator/streaming is injected as a closure, matching the DI
// convention used throughout this codebase.
package adminsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/lumena/admin"
)

// ─── Injected closures ──────────────────────────────────────────────────────

// GetAccountViewFunc adapts authsvc into a read-only account summary.
type GetAccountViewFunc func(ctx context.Context, accountID string) (*admin.AccountView, error)

// SetAccountStatusFunc adapts authsvc.Service.SetAccountStatus. status is
// one of "restricted" or "active" — admin console never issues "suspended"
// or "terminate" (that stays moderation's rule-bound enforcement path);
// admin's restrict/unsuspend is the ops-support lever, not T&S enforcement.
type SetAccountStatusFunc func(ctx context.Context, accountID, status string) error

// ListGiftTransactionsFunc adapts ledger.Repo (via MemLedger.ListByKind) —
// read-only, no ledger-writing closure is ever injected into this
// package, which is how "admin cannot edit ledger entries directly"
// (roadmap exit gate) is actually enforced: there is no code path here
// that could.
type ListGiftTransactionsFunc func(ctx context.Context, since time.Time) ([]GiftFlow, error)

type GiftFlow struct {
	SenderID   string
	RecipientID string
	CreatedAt  time.Time
}

type LedgerIntegrityFunc func(ctx context.Context) (map[string]int64, error)
type ActiveStreamsCountFunc func(ctx context.Context) (int, error)
type OpenReportsCountFunc func(ctx context.Context) (int, error)
type FlaggedRiskFunc func(ctx context.Context) ([]admin.RiskFlag, error)
type PendingPayoutsCountFunc func(ctx context.Context) (int, error)

// ListPayoutsFunc adapts creatorsvc.Service.AdminListPayouts — statusFilter
// "" means all statuses.
type ListPayoutsFunc func(ctx context.Context, statusFilter string) ([]admin.PayoutView, error)

// PayoutActionFunc adapts one of creatorsvc.Service's admin payout
// transition methods (Approve/Reject/MarkProcessing/MarkPaid/MarkFailed).
// action is one of "approve"|"reject"|"processing"|"paid"|"failed"; note
// carries the reject/failed reason or the paid payout_reference, and is
// unused for "processing". A single closure rather than five keeps the
// composition-root wiring and the Service constructor from ballooning —
// creatorsvc still exposes five separate typed methods internally.
type PayoutActionFunc func(ctx context.Context, action, payoutID, adminID, note string) (*admin.PayoutView, error)

// GetTransactionFunc adapts ledger.Repo.GetTransactionByID — read-only, so
// an admin can review what a transaction actually did before deciding
// whether to reverse it.
type GetTransactionFunc func(ctx context.Context, transactionID string) (*admin.TransactionView, error)

// ReverseTransactionFunc adapts ledger.Repo.ReverseTransaction — the only
// ledger-writing closure this package is ever given, and it can only
// reverse an existing transaction (post its exact inverse), never edit a
// balance or entry directly.
type ReverseTransactionFunc func(ctx context.Context, transactionID, idempotencyKey, reason string) (*admin.TransactionView, error)

// ─── Repos ──────────────────────────────────────────────────────────────────

type RoleRepo interface {
	GetRole(ctx context.Context, accountID string) (admin.Role, bool, error)
	SetRole(ctx context.Context, accountID string, role admin.Role) error
}

type AuditRepo interface {
	Append(ctx context.Context, e admin.AuditEntry) (*admin.AuditEntry, error)
	List(ctx context.Context, limit int) ([]admin.AuditEntry, error)
}

type TicketRepo interface {
	Create(ctx context.Context, t admin.SupportTicket, firstMessage admin.TicketMessage) (*admin.SupportTicket, error)
	Get(ctx context.Context, id string) (*admin.SupportTicket, error)
	ListByAccount(ctx context.Context, accountID string) ([]admin.SupportTicket, error)
	ListAll(ctx context.Context, statusFilter admin.TicketStatus) ([]admin.SupportTicket, error)
	CountOpen(ctx context.Context) (int, error)
	UpdateStatusAssignee(ctx context.Context, id string, status admin.TicketStatus, assignedTo string) error
	AddMessage(ctx context.Context, ticketID string, m admin.TicketMessage) (*admin.TicketMessage, error)
	ListMessages(ctx context.Context, ticketID string) ([]admin.TicketMessage, error)
}

// ─── Service ────────────────────────────────────────────────────────────────

type Service struct {
	roles   RoleRepo
	audit   AuditRepo
	tickets TicketRepo

	getAccount      GetAccountViewFunc
	setAccountStatus SetAccountStatusFunc
	listGifts       ListGiftTransactionsFunc
	ledgerIntegrity LedgerIntegrityFunc
	activeStreams   ActiveStreamsCountFunc
	openReports     OpenReportsCountFunc
	flaggedRisk     FlaggedRiskFunc
	pendingPayouts  PendingPayoutsCountFunc
	listPayouts     ListPayoutsFunc
	payoutAction    PayoutActionFunc
	getTransaction      GetTransactionFunc
	reverseTransaction  ReverseTransactionFunc

	now func() time.Time
}

func New(
	roles RoleRepo, audit AuditRepo, tickets TicketRepo,
	getAccount GetAccountViewFunc, setAccountStatus SetAccountStatusFunc,
	listGifts ListGiftTransactionsFunc, ledgerIntegrity LedgerIntegrityFunc,
	activeStreams ActiveStreamsCountFunc, openReports OpenReportsCountFunc,
	flaggedRisk FlaggedRiskFunc, pendingPayouts PendingPayoutsCountFunc,
	listPayouts ListPayoutsFunc, payoutAction PayoutActionFunc,
	getTransaction GetTransactionFunc, reverseTransaction ReverseTransactionFunc,
) *Service {
	return &Service{
		roles: roles, audit: audit, tickets: tickets,
		getAccount: getAccount, setAccountStatus: setAccountStatus,
		listGifts: listGifts, ledgerIntegrity: ledgerIntegrity,
		activeStreams: activeStreams, openReports: openReports,
		flaggedRisk: flaggedRisk, pendingPayouts: pendingPayouts,
		listPayouts: listPayouts, payoutAction: payoutAction,
		getTransaction: getTransaction, reverseTransaction: reverseTransaction,
		now: time.Now,
	}
}

func (s *Service) requirePermission(ctx context.Context, adminID string, perm admin.Permission) (admin.Role, error) {
	role, ok, err := s.roles.GetRole(ctx, adminID)
	if err != nil {
		return "", err
	}
	if !ok || !admin.RoleHasPermission(role, perm) {
		return "", admin.ErrForbidden
	}
	return role, nil
}

func (s *Service) logAction(ctx context.Context, actorID string, role admin.Role, action, targetType, targetID, detail string) {
	_, _ = s.audit.Append(ctx, admin.AuditEntry{
		ActorID: actorID, ActorRole: role, Action: action,
		TargetType: targetType, TargetID: targetID, Detail: detail, CreatedAt: s.now(),
	})
}

// HasPermission is the read-only RBAC check other modules (analytics,
// Phase 17) can wire as a closure — same rule table requirePermission uses
// internally, exposed without also exposing role-mutation.
func (s *Service) HasPermission(ctx context.Context, actorID string, perm admin.Permission) (bool, error) {
	role, ok, err := s.roles.GetRole(ctx, actorID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return admin.RoleHasPermission(role, perm), nil
}

// ─── RBAC (dev-only escalation) ─────────────────────────────────────────────

// DevGrantRole grants accountID an admin role, standing in for the real
// staff IdP/SSO integration that doesn't exist yet — same dev-escalation
// pattern as moderation's DevGrantModerator. NEVER ships to production.
func (s *Service) DevGrantRole(ctx context.Context, accountID string, role admin.Role) error {
	if !admin.ValidRole(role) {
		return admin.ErrInvalidRole
	}
	return s.roles.SetRole(ctx, accountID, role)
}

func (s *Service) GetRole(ctx context.Context, accountID string) (admin.Role, bool, error) {
	return s.roles.GetRole(ctx, accountID)
}

// ─── Account management ─────────────────────────────────────────────────────

func (s *Service) GetAccount(ctx context.Context, adminID, accountID string) (*admin.AccountView, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermAccountsManage); err != nil {
		return nil, err
	}
	return s.getAccount(ctx, accountID)
}

func (s *Service) RestrictAccount(ctx context.Context, adminID, accountID, reason string) error {
	role, err := s.requirePermission(ctx, adminID, admin.PermAccountsManage)
	if err != nil {
		return err
	}
	if err := s.setAccountStatus(ctx, accountID, "restricted"); err != nil {
		return err
	}
	s.logAction(ctx, adminID, role, "restrict_account", "account", accountID, reason)
	return nil
}

func (s *Service) UnsuspendAccount(ctx context.Context, adminID, accountID string) error {
	role, err := s.requirePermission(ctx, adminID, admin.PermAccountsManage)
	if err != nil {
		return err
	}
	if err := s.setAccountStatus(ctx, accountID, "active"); err != nil {
		return err
	}
	s.logAction(ctx, adminID, role, "unsuspend_account", "account", accountID, "")
	return nil
}

// ─── Economy oversight (read-only, AF-03) ───────────────────────────────────

// ListEconomyAnomalies scans recent gift transactions for A→B / B→A
// circular flows within window — doc 02 AF-03's "gift-loop / circular-flow
// detection (earnings washing)". Read-only: this method has no write path
// into the ledger.
func (s *Service) ListEconomyAnomalies(ctx context.Context, adminID string, window time.Duration) ([]admin.EconomyAnomaly, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermEconomyView); err != nil {
		return nil, err
	}
	flows, err := s.listGifts(ctx, s.now().Add(-window))
	if err != nil {
		return nil, err
	}

	type pair struct{ a, b string }
	sentAToB := map[pair]bool{}
	for _, f := range flows {
		sentAToB[pair{f.SenderID, f.RecipientID}] = true
	}

	seen := map[pair]bool{}
	var anomalies []admin.EconomyAnomaly
	for p := range sentAToB {
		reverse := pair{p.b, p.a}
		if !sentAToB[reverse] {
			continue
		}
		key := pair{p.a, p.b}
		if p.a > p.b {
			key = pair{p.b, p.a}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		anomalies = append(anomalies, admin.EconomyAnomaly{
			Type: "gift_loop", AccountA: key.a, AccountB: key.b,
			Detail:     fmt.Sprintf("%s and %s sent gifts to each other within the last %s", key.a, key.b, window),
			DetectedAt: s.now(),
		})
	}
	return anomalies, nil
}

// ─── Fraud review queue ─────────────────────────────────────────────────────

func (s *Service) ListFraudQueue(ctx context.Context, adminID string) ([]admin.RiskFlag, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermFraudReview); err != nil {
		return nil, err
	}
	return s.flaggedRisk(ctx)
}

// ─── Support tickets (BT-08) ─────────────────────────────────────────────────

func (s *Service) CreateTicket(ctx context.Context, accountID, subject, category, body string) (*admin.SupportTicket, error) {
	now := s.now()
	return s.tickets.Create(ctx, admin.SupportTicket{
		AccountID: accountID, Subject: subject, Category: category,
		Status: admin.TicketOpen, SLADeadline: now.Add(admin.FirstResponseSLA),
		CreatedAt: now, UpdatedAt: now,
	}, admin.TicketMessage{AuthorID: accountID, IsAdmin: false, Body: body, CreatedAt: now})
}

func (s *Service) ListMyTickets(ctx context.Context, accountID string) ([]admin.SupportTicket, error) {
	return s.tickets.ListByAccount(ctx, accountID)
}

func (s *Service) GetTicket(ctx context.Context, requesterID, ticketID string) (*admin.SupportTicket, []admin.TicketMessage, error) {
	t, err := s.tickets.Get(ctx, ticketID)
	if err != nil {
		return nil, nil, err
	}
	if t.AccountID != requesterID {
		if _, err := s.requirePermission(ctx, requesterID, admin.PermSupportManage); err != nil {
			return nil, nil, admin.ErrNotTicketParty
		}
	}
	msgs, err := s.tickets.ListMessages(ctx, ticketID)
	if err != nil {
		return nil, nil, err
	}
	return t, msgs, nil
}

// ReplyTicket lets either the ticket's owner or a support-permission admin
// add a message. A user reply reopens the ticket (status -> open, awaiting
// support); a support reply marks it pending (awaiting the user).
func (s *Service) ReplyTicket(ctx context.Context, authorID, ticketID, body string) (*admin.TicketMessage, error) {
	t, err := s.tickets.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	isAdmin := false
	if t.AccountID != authorID {
		if _, err := s.requirePermission(ctx, authorID, admin.PermSupportManage); err != nil {
			return nil, admin.ErrNotTicketParty
		}
		isAdmin = true
	}
	msg, err := s.tickets.AddMessage(ctx, ticketID, admin.TicketMessage{
		AuthorID: authorID, IsAdmin: isAdmin, Body: body, CreatedAt: s.now(),
	})
	if err != nil {
		return nil, err
	}
	// Admin reply -> pending (awaiting the user); user reply -> open
	// (awaiting support) — the queue's status column always shows whose
	// turn it is to respond.
	nextStatus := admin.TicketOpen
	if isAdmin {
		nextStatus = admin.TicketPending
	}
	_ = s.tickets.UpdateStatusAssignee(ctx, ticketID, nextStatus, "")
	return msg, nil
}

// ListTicketQueue is the support escalation view (moderator-only) with SLA
// visibility — doc 03 SUPP_002-adjacent, but for staff rather than the
// ticket's own owner.
func (s *Service) ListTicketQueue(ctx context.Context, adminID string, statusFilter admin.TicketStatus) ([]admin.SupportTicket, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermSupportManage); err != nil {
		return nil, err
	}
	return s.tickets.ListAll(ctx, statusFilter)
}

func (s *Service) ResolveTicket(ctx context.Context, adminID, ticketID string) error {
	role, err := s.requirePermission(ctx, adminID, admin.PermSupportManage)
	if err != nil {
		return err
	}
	if err := s.tickets.UpdateStatusAssignee(ctx, ticketID, admin.TicketResolved, adminID); err != nil {
		return err
	}
	s.logAction(ctx, adminID, role, "resolve_ticket", "ticket", ticketID, "")
	return nil
}

// ─── Platform health ─────────────────────────────────────────────────────

func (s *Service) GetPlatformHealth(ctx context.Context, adminID string) (*admin.PlatformHealth, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermHealthView); err != nil {
		return nil, err
	}
	h := &admin.PlatformHealth{}
	var err error
	if h.ActiveStreams, err = s.activeStreams(ctx); err != nil {
		return nil, err
	}
	if h.OpenReports, err = s.openReports(ctx); err != nil {
		return nil, err
	}
	flagged, err := s.flaggedRisk(ctx)
	if err != nil {
		return nil, err
	}
	h.FlaggedRiskAccounts = len(flagged)
	if h.PendingPayouts, err = s.pendingPayouts(ctx); err != nil {
		return nil, err
	}
	if h.OpenTickets, err = s.tickets.CountOpen(ctx); err != nil {
		return nil, err
	}
	if h.LedgerIntegrity, err = s.ledgerIntegrity(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// ─── Withdrawal management (finance role) ───────────────────────────────────
//
// Admin never touches a wallet balance or ledger entry directly here (doc
// rule 20/"Admin should NOT directly modify wallet balances") — every
// action below delegates the actual state change (and, for reject/fail,
// the compensating ledger reversal) to creatorsvc via the injected
// payoutAction closure, then records its own audit-log entry. Same
// division of responsibility as RestrictAccount delegating to authsvc.

func (s *Service) ListPayouts(ctx context.Context, adminID, statusFilter string) ([]admin.PayoutView, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage); err != nil {
		return nil, err
	}
	return s.listPayouts(ctx, statusFilter)
}

func (s *Service) ApprovePayout(ctx context.Context, adminID, payoutID, note string) (*admin.PayoutView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage)
	if err != nil {
		return nil, err
	}
	pv, err := s.payoutAction(ctx, "approve", payoutID, adminID, note)
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "approve_payout", "payout", payoutID, note)
	return pv, nil
}

func (s *Service) RejectPayout(ctx context.Context, adminID, payoutID, reason string) (*admin.PayoutView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage)
	if err != nil {
		return nil, err
	}
	pv, err := s.payoutAction(ctx, "reject", payoutID, adminID, reason)
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "reject_payout", "payout", payoutID, reason)
	return pv, nil
}

func (s *Service) MarkPayoutProcessing(ctx context.Context, adminID, payoutID string) (*admin.PayoutView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage)
	if err != nil {
		return nil, err
	}
	pv, err := s.payoutAction(ctx, "processing", payoutID, adminID, "")
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "mark_payout_processing", "payout", payoutID, "")
	return pv, nil
}

func (s *Service) MarkPayoutPaid(ctx context.Context, adminID, payoutID, payoutReference string) (*admin.PayoutView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage)
	if err != nil {
		return nil, err
	}
	pv, err := s.payoutAction(ctx, "paid", payoutID, adminID, payoutReference)
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "mark_payout_paid", "payout", payoutID, "payout_reference="+payoutReference)
	return pv, nil
}

func (s *Service) MarkPayoutFailed(ctx context.Context, adminID, payoutID, reason string) (*admin.PayoutView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermPayoutManage)
	if err != nil {
		return nil, err
	}
	pv, err := s.payoutAction(ctx, "failed", payoutID, adminID, reason)
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "mark_payout_failed", "payout", payoutID, reason)
	return pv, nil
}

// ─── Refunds / transaction reversal (finance role) ──────────────────────────
//
// Refunding never deletes or edits the original transaction (doc rule 15) —
// GetLedgerTransaction is read-only so an admin can review what actually
// happened, and ReverseLedgerTransaction only ever posts a new compensating
// transaction via the ledger's own ReverseTransaction, never touches a
// balance directly.

func (s *Service) GetLedgerTransaction(ctx context.Context, adminID, transactionID string) (*admin.TransactionView, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermRefundManage); err != nil {
		return nil, err
	}
	return s.getTransaction(ctx, transactionID)
}

// ReverseLedgerTransaction issues a refund: it posts the exact inverse of
// transactionID's entries as a new ledger transaction, idempotent on
// idempotencyKey. reason is required and recorded both on the reversal
// transaction's metadata and in the admin audit log.
func (s *Service) ReverseLedgerTransaction(ctx context.Context, adminID, transactionID, idempotencyKey, reason string) (*admin.TransactionView, error) {
	role, err := s.requirePermission(ctx, adminID, admin.PermRefundManage)
	if err != nil {
		return nil, err
	}
	tv, err := s.reverseTransaction(ctx, transactionID, idempotencyKey, reason)
	if err != nil {
		return nil, err
	}
	s.logAction(ctx, adminID, role, "reverse_transaction", "transaction", transactionID, reason)
	return tv, nil
}

// ─── Audit log ──────────────────────────────────────────────────────────────

func (s *Service) ListAuditLog(ctx context.Context, adminID string, limit int) ([]admin.AuditEntry, error) {
	if _, err := s.requirePermission(ctx, adminID, admin.PermAuditView); err != nil {
		return nil, err
	}
	return s.audit.List(ctx, limit)
}
