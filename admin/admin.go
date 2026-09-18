// Package admin implements the admin dashboard (doc 02 PL-08, roadmap
// Phase 16): RBAC-gated account management, read-only economy oversight,
// a fraud review queue, support-ticket escalation, and a platform health
// view — plus the audit log every mutating action writes to.
//
// Like moderation and creator before it, this package never imports auth,
// ledger or moderation directly — the composition root adapts those
// services into the closures declared in adminsvc.
package admin

import (
	"errors"
	"time"
)

// ─── RBAC ───────────────────────────────────────────────────────────────────

type Role string

const (
	RoleSupport     Role = "support"
	RoleFinance     Role = "finance"
	RoleTrustSafety Role = "trust_safety"
	RoleSuperAdmin  Role = "superadmin"
)

type Permission string

const (
	PermAccountsManage Permission = "accounts.manage"
	PermEconomyView    Permission = "economy.view"
	PermFraudReview    Permission = "fraud.review"
	PermSupportManage  Permission = "support.manage"
	PermHealthView     Permission = "health.view"
	PermAuditView      Permission = "audit.view"
	PermAnalyticsView  Permission = "analytics.view"
	PermRolloutManage  Permission = "rollout.manage"
	// PermPayoutManage gates the withdrawal-management console: reviewing,
	// approving, rejecting and marking creator payouts paid. Finance-role
	// territory (with superadmin override), same shape as every other
	// permission here.
	PermPayoutManage  Permission = "payout.manage"
	// PermRefundManage gates issuing a refund/reversal of a ledger
	// transaction — deliberately separate from PermEconomyView (which is
	// read-only) so "admin can look at the economy" and "admin can move
	// money" stay two different grants.
	PermRefundManage  Permission = "refund.manage"
)

// RolePermissions is the whole RBAC table — the exit gate ("admin cannot
// perform actions outside their role") is enforced by every mutating
// adminsvc method checking against exactly this map, never a broader or
// role-independent check.
var RolePermissions = map[Role]map[Permission]bool{
	RoleSupport: {
		PermSupportManage: true,
	},
	RoleFinance: {
		PermEconomyView:   true,
		PermAuditView:     true,
		PermAnalyticsView: true,
		PermPayoutManage:  true,
		PermRefundManage:  true,
	},
	RoleTrustSafety: {
		PermAccountsManage: true,
		PermFraudReview:    true,
		PermAuditView:      true,
	},
	RoleSuperAdmin: {
		PermAccountsManage: true,
		PermEconomyView:    true,
		PermFraudReview:    true,
		PermSupportManage:  true,
		PermHealthView:     true,
		PermAuditView:      true,
		PermAnalyticsView:  true,
		PermRolloutManage:  true,
		PermPayoutManage:   true,
		PermRefundManage:   true,
	},
}

func ValidRole(r Role) bool {
	_, ok := RolePermissions[r]
	return ok
}

func RoleHasPermission(r Role, p Permission) bool {
	return RolePermissions[r][p]
}

// ─── Audit log ──────────────────────────────────────────────────────────────

// AuditEntry is written for every mutating admin action — "Admin actions
// are audit-logged" (roadmap Phase 16 exit gate). Append-only, like
// ledger_entries and moderation's enforcements: no update/delete method
// exists on AuditRepo.
type AuditEntry struct {
	ID         string    `json:"id"`
	ActorID    string    `json:"actor_id"`
	ActorRole  Role      `json:"actor_role"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Detail     string    `json:"detail,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ─── Account management ─────────────────────────────────────────────────────

type AccountView struct {
	AccountID  string    `json:"account_id"`
	Status     string    `json:"status"`
	AgeStatus  string    `json:"age_status"`
	CreatedAt  time.Time `json:"created_at"`
}

// ─── Economy oversight (AF-03) ──────────────────────────────────────────────

type EconomyAnomaly struct {
	Type      string    `json:"type"` // "gift_loop"
	AccountA  string    `json:"account_a"`
	AccountB  string    `json:"account_b"`
	Detail    string    `json:"detail"`
	DetectedAt time.Time `json:"detected_at"`
}

// ─── Fraud review queue ──────────────────────────────────────────────────
//
// Deliberately the same shape as moderation.RiskProfile (doc 06 §12's
// risk_profiles table) — admin's fraud queue is a read-only RBAC-gated view
// over the same signals moderation's risk engine already computes, not a
// second parallel scoring system.

type RiskFlag struct {
	AccountID    string   `json:"account_id"`
	RiskScore    float64  `json:"risk_score"`
	RiskReasons  []string `json:"risk_reasons"`
	ReviewStatus string   `json:"review_status"`
}

// ─── Support tickets (BT-08, SUPP_001-003) ──────────────────────────────────

type TicketStatus string

const (
	TicketOpen     TicketStatus = "open"     // awaiting a support reply
	TicketPending  TicketStatus = "pending"  // support replied, awaiting the user
	TicketResolved TicketStatus = "resolved"
)

// FirstResponseSLA is the dev-scale SLA used to compute a ticket's
// deadline — doc 01 BT-08 requires a visible SLA timer, not a specific
// duration.
const FirstResponseSLA = 24 * time.Hour

type SupportTicket struct {
	ID          string       `json:"id"`
	AccountID   string       `json:"account_id"`
	Subject     string       `json:"subject"`
	Category    string       `json:"category"`
	Status      TicketStatus `json:"status"`
	AssignedTo  string       `json:"assigned_to,omitempty"`
	SLADeadline time.Time    `json:"sla_deadline"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type TicketMessage struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	AuthorID  string    `json:"author_id"`
	IsAdmin   bool      `json:"is_admin"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// ─── Platform health ─────────────────────────────────────────────────────

type PlatformHealth struct {
	ActiveStreams      int            `json:"active_streams"`
	OpenReports        int            `json:"open_reports"`
	FlaggedRiskAccounts int           `json:"flagged_risk_accounts"`
	PendingPayouts     int            `json:"pending_payouts"`
	OpenTickets        int            `json:"open_tickets"`
	LedgerIntegrity    map[string]int64 `json:"ledger_integrity"` // currency -> sum across all accounts; must be 0
}

// ─── Withdrawal management (finance role) ───────────────────────────────────
//
// PayoutView mirrors the subset of creator.Payout the admin console needs —
// deliberately a local shadow type (same convention as RiskFlag mirroring
// moderation.RiskProfile) rather than importing the creator package
// directly, so admin's dependency graph stays closure-only.
type PayoutView struct {
	ID                    string     `json:"id"`
	AccountID             string     `json:"account_id"`
	AmountDiamonds        int64      `json:"amount_diamonds"`
	Status                string     `json:"status"`
	PayoutReference       string     `json:"payout_reference,omitempty"`
	AdminNote             string     `json:"admin_note,omitempty"`
	FailureReason         string     `json:"failure_reason,omitempty"`
	ReviewedBy            string     `json:"reviewed_by,omitempty"`
	RequestedAt           time.Time  `json:"requested_at"`
	ReviewedAt            *time.Time `json:"reviewed_at,omitempty"`
	PaidAt                *time.Time `json:"paid_at,omitempty"`
}

// ─── Refunds / transaction reversal (finance role) ─────────────────────────
//
// TransactionView mirrors the subset of ledger.Transaction the admin
// console needs to review before reversing it — same local-shadow-type
// convention as PayoutView.
type TransactionView struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Entries   []LedgerEntryView `json:"entries"`
	CreatedAt time.Time      `json:"created_at"`
}

type LedgerEntryView struct {
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
}

var (
	ErrForbidden       = errors.New("admin: this role does not have the required permission")
	ErrInvalidRole     = errors.New("admin: unknown role")
	ErrTicketNotFound  = errors.New("admin: ticket not found")
	ErrNotTicketParty  = errors.New("admin: not a party to this ticket")
	// ErrPayoutNotFound and ErrInvalidPayoutTransition are what the
	// composition root's PayoutActionFunc closure translates
	// creator.ErrPayoutNotFound / creator.ErrInvalidPayoutTransition into,
	// so this package's HTTP layer can map them to the right status code
	// without importing the creator package directly.
	ErrPayoutNotFound          = errors.New("admin: payout not found")
	ErrInvalidPayoutTransition = errors.New("admin: payout is not in a state that allows this action")
	// ErrTransactionNotFound, ErrAlreadyReversed and ErrCannotReverse are
	// what the composition root's ReverseTransactionFunc closure translates
	// the matching ledger sentinel errors into, for the same reason.
	ErrTransactionNotFound = errors.New("admin: transaction not found")
	ErrAlreadyReversed     = errors.New("admin: this transaction has already been reversed")
	ErrCannotReverse       = errors.New("admin: this transaction cannot be reversed")
	// ErrReversalWouldOverdraw is what ReverseTransactionFunc translates a
	// *ledger.InsufficientBalanceError into — the coins/diamonds being
	// clawed back were already spent elsewhere, so the reversal can't be
	// applied without driving some account negative.
	ErrReversalWouldOverdraw = errors.New("admin: reversing this transaction would overdraw an account (funds already spent elsewhere)")
)
