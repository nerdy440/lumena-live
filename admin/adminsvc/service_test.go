package adminsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lumena/admin"
	"github.com/lumena/admin/adminsvc"
)

type fakeAuth struct {
	status map[string]string
}

func newFakeAuth() *fakeAuth { return &fakeAuth{status: map[string]string{}} }

func (f *fakeAuth) getAccount(_ context.Context, accountID string) (*admin.AccountView, error) {
	return &admin.AccountView{AccountID: accountID, Status: f.status[accountID], AgeStatus: "declared", CreatedAt: time.Now()}, nil
}
func (f *fakeAuth) setStatus(_ context.Context, accountID, status string) error {
	f.status[accountID] = status
	return nil
}

// fakePayouts is a minimal in-memory stand-in for creatorsvc's admin payout
// methods, just enough to exercise adminsvc's RBAC gating and audit-log
// wiring without pulling in the creator module.
type fakePayouts struct {
	byID map[string]*admin.PayoutView
}

func newFakePayouts() *fakePayouts {
	return &fakePayouts{byID: map[string]*admin.PayoutView{
		"payout-1": {ID: "payout-1", AccountID: "creator1", AmountDiamonds: 10000, Status: "requested"},
	}}
}

func (f *fakePayouts) list(_ context.Context, statusFilter string) ([]admin.PayoutView, error) {
	var out []admin.PayoutView
	for _, p := range f.byID {
		if statusFilter == "" || p.Status == statusFilter {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakePayouts) action(_ context.Context, action, payoutID, adminID, note string) (*admin.PayoutView, error) {
	p, ok := f.byID[payoutID]
	if !ok {
		return nil, errors.New("payout not found")
	}
	switch action {
	case "approve":
		p.Status = "approved"
	case "reject":
		p.Status = "rejected"
	case "processing":
		p.Status = "processing"
	case "paid":
		p.Status = "paid"
		p.PayoutReference = note
	case "failed":
		p.Status = "failed"
	}
	p.ReviewedBy = adminID
	cp := *p
	return &cp, nil
}

// fakeTransactions is a minimal in-memory stand-in for a ledger repo's
// GetTransactionByID/ReverseTransaction, just enough to exercise adminsvc's
// refund RBAC gating and audit-log wiring without pulling in the ledger
// module.
type fakeTransactions struct {
	byID     map[string]*admin.TransactionView
	reversed map[string]bool
}

func newFakeTransactions() *fakeTransactions {
	return &fakeTransactions{
		byID: map[string]*admin.TransactionView{
			"tx-1": {ID: "tx-1", Kind: "gift", Entries: []admin.LedgerEntryView{
				{AccountID: "user:alice:coins", Amount: -100, Currency: "COIN"},
				{AccountID: "user:bob:diamonds", Amount: 70, Currency: "DIAMOND"},
			}},
		},
		reversed: map[string]bool{},
	}
}

func (f *fakeTransactions) get(_ context.Context, id string) (*admin.TransactionView, error) {
	tv, ok := f.byID[id]
	if !ok {
		return nil, errors.New("transaction not found")
	}
	return tv, nil
}

func (f *fakeTransactions) reverse(_ context.Context, id, _, _ string) (*admin.TransactionView, error) {
	if _, ok := f.byID[id]; !ok {
		return nil, errors.New("transaction not found")
	}
	if f.reversed[id] {
		return nil, errors.New("already reversed")
	}
	f.reversed[id] = true
	return &admin.TransactionView{ID: "tx-reversal-1", Kind: "reversal"}, nil
}

func newTestService() (*adminsvc.Service, *fakeAuth) {
	auth := newFakeAuth()
	payouts := newFakePayouts()
	txns := newFakeTransactions()
	svc := adminsvc.New(
		admin.NewMemRoleRepo(), admin.NewMemAuditRepo(), admin.NewMemTicketRepo(),
		auth.getAccount, auth.setStatus,
		func(context.Context, time.Time) ([]adminsvc.GiftFlow, error) { return nil, nil },
		func(context.Context) (map[string]int64, error) { return map[string]int64{"COIN": 0, "DIAMOND": 0}, nil },
		func(context.Context) (int, error) { return 0, nil },
		func(context.Context) (int, error) { return 0, nil },
		func(context.Context) ([]admin.RiskFlag, error) { return nil, nil },
		func(context.Context) (int, error) { return 0, nil },
		payouts.list, payouts.action,
		txns.get, txns.reverse,
	)
	return svc, auth
}

func TestRBAC_SupportCannotManageAccounts(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "staff1", admin.RoleSupport); err != nil {
		t.Fatal(err)
	}
	err := svc.RestrictAccount(ctx, "staff1", "bob", "spam")
	if !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for support role restricting an account, got %v", err)
	}
}

func TestRBAC_FinanceCannotManageAccounts(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "staff1", admin.RoleFinance); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetAccount(ctx, "staff1", "bob"); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for finance role viewing an account, got %v", err)
	}
	// But finance CAN view economy.
	if _, err := svc.ListEconomyAnomalies(ctx, "staff1", 24*time.Hour); err != nil {
		t.Fatalf("expected finance to access economy view, got %v", err)
	}
}

func TestRBAC_TrustSafetyCannotManageSupport(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "staff1", admin.RoleTrustSafety); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListTicketQueue(ctx, "staff1", ""); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for trust_safety listing ticket queue, got %v", err)
	}
}

func TestRBAC_UngrantedAccountForbiddenEverywhere(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if _, err := svc.GetAccount(ctx, "rando", "bob"); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for an account with no granted role, got %v", err)
	}
}

func TestRestrictAndUnsuspendAccount_AppliesAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, auth := newTestService()
	if err := svc.DevGrantRole(ctx, "staff1", admin.RoleTrustSafety); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestrictAccount(ctx, "staff1", "bob", "suspicious activity"); err != nil {
		t.Fatal(err)
	}
	if auth.status["bob"] != "restricted" {
		t.Fatalf("expected bob restricted, got %v", auth.status)
	}

	if err := svc.DevGrantRole(ctx, "root", admin.RoleSuperAdmin); err != nil {
		t.Fatal(err)
	}
	log, err := svc.ListAuditLog(ctx, "root", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Action != "restrict_account" || log[0].TargetID != "bob" || log[0].ActorID != "staff1" {
		t.Fatalf("expected one audit entry for the restrict action, got %+v", log)
	}

	if err := svc.UnsuspendAccount(ctx, "staff1", "bob"); err != nil {
		t.Fatal(err)
	}
	if auth.status["bob"] != "active" {
		t.Fatalf("expected bob active, got %v", auth.status)
	}
	log, err = svc.ListAuditLog(ctx, "root", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 {
		t.Fatalf("expected two audit entries after unsuspend, got %d", len(log))
	}
}

func TestPayoutManagement_RequiresPermissionAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()

	// Trust & safety can't touch payouts — that's finance/superadmin only.
	if err := svc.DevGrantRole(ctx, "ts1", admin.RoleTrustSafety); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListPayouts(ctx, "ts1", ""); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for trust_safety listing payouts, got %v", err)
	}

	if err := svc.DevGrantRole(ctx, "fin1", admin.RoleFinance); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListPayouts(ctx, "fin1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "payout-1" {
		t.Fatalf("expected the one seeded payout, got %+v", list)
	}

	approved, err := svc.ApprovePayout(ctx, "fin1", "payout-1", "looks legit")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.ReviewedBy != "fin1" {
		t.Fatalf("unexpected approved payout: %+v", approved)
	}

	if _, err := svc.MarkPayoutProcessing(ctx, "fin1", "payout-1"); err != nil {
		t.Fatal(err)
	}
	paid, err := svc.MarkPayoutPaid(ctx, "fin1", "payout-1", "bank-ref-42")
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != "paid" || paid.PayoutReference != "bank-ref-42" {
		t.Fatalf("unexpected paid payout: %+v", paid)
	}

	if err := svc.DevGrantRole(ctx, "root", admin.RoleSuperAdmin); err != nil {
		t.Fatal(err)
	}
	log, err := svc.ListAuditLog(ctx, "root", 10)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, e := range log {
		actions[e.Action] = true
	}
	for _, want := range []string{"approve_payout", "mark_payout_processing", "mark_payout_paid"} {
		if !actions[want] {
			t.Fatalf("expected audit log to contain %q, got %+v", want, log)
		}
	}
}

func TestRefundManagement_RequiresPermissionAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()

	// Trust & safety can't issue refunds — that's finance/superadmin only.
	if err := svc.DevGrantRole(ctx, "ts1", admin.RoleTrustSafety); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetLedgerTransaction(ctx, "ts1", "tx-1"); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for trust_safety viewing a transaction, got %v", err)
	}

	if err := svc.DevGrantRole(ctx, "fin1", admin.RoleFinance); err != nil {
		t.Fatal(err)
	}
	tv, err := svc.GetLedgerTransaction(ctx, "fin1", "tx-1")
	if err != nil {
		t.Fatal(err)
	}
	if tv.ID != "tx-1" || tv.Kind != "gift" {
		t.Fatalf("unexpected transaction view: %+v", tv)
	}

	reversal, err := svc.ReverseLedgerTransaction(ctx, "fin1", "tx-1", "idem-1", "chargeback")
	if err != nil {
		t.Fatal(err)
	}
	if reversal.Kind != "reversal" {
		t.Fatalf("unexpected reversal: %+v", reversal)
	}

	// A second reversal of the same transaction is rejected.
	if _, err := svc.ReverseLedgerTransaction(ctx, "fin1", "tx-1", "idem-2", "again"); err == nil {
		t.Fatal("expected an error reversing an already-reversed transaction")
	}

	if err := svc.DevGrantRole(ctx, "root", admin.RoleSuperAdmin); err != nil {
		t.Fatal(err)
	}
	log, err := svc.ListAuditLog(ctx, "root", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range log {
		if e.Action == "reverse_transaction" && e.TargetID == "tx-1" && e.Detail == "chargeback" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an audit entry for the reversal, got %+v", log)
	}
}

func TestListEconomyAnomalies_DetectsGiftLoop(t *testing.T) {
	ctx := context.Background()
	auth := newFakeAuth()
	svc := adminsvc.New(
		admin.NewMemRoleRepo(), admin.NewMemAuditRepo(), admin.NewMemTicketRepo(),
		auth.getAccount, auth.setStatus,
		func(context.Context, time.Time) ([]adminsvc.GiftFlow, error) {
			return []adminsvc.GiftFlow{
				{SenderID: "alice", RecipientID: "bob", CreatedAt: time.Now()},
				{SenderID: "bob", RecipientID: "alice", CreatedAt: time.Now()},
				{SenderID: "carol", RecipientID: "dave", CreatedAt: time.Now()}, // one-directional, not a loop
			}, nil
		},
		func(context.Context) (map[string]int64, error) { return nil, nil },
		func(context.Context) (int, error) { return 0, nil },
		func(context.Context) (int, error) { return 0, nil },
		func(context.Context) ([]admin.RiskFlag, error) { return nil, nil },
		func(context.Context) (int, error) { return 0, nil },
		func(context.Context, string) ([]admin.PayoutView, error) { return nil, nil },
		func(context.Context, string, string, string, string) (*admin.PayoutView, error) { return nil, nil },
		func(context.Context, string) (*admin.TransactionView, error) { return nil, nil },
		func(context.Context, string, string, string) (*admin.TransactionView, error) { return nil, nil },
	)
	if err := svc.DevGrantRole(ctx, "fin1", admin.RoleFinance); err != nil {
		t.Fatal(err)
	}
	anomalies, err := svc.ListEconomyAnomalies(ctx, "fin1", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(anomalies) != 1 {
		t.Fatalf("expected exactly one gift-loop anomaly (alice<->bob), got %+v", anomalies)
	}
	if anomalies[0].Type != "gift_loop" {
		t.Fatalf("expected gift_loop type, got %s", anomalies[0].Type)
	}
}

func TestSupportTicket_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "support1", admin.RoleSupport); err != nil {
		t.Fatal(err)
	}

	ticket, err := svc.CreateTicket(ctx, "alice", "Missing coins", "billing", "I paid but coins never arrived")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Status != admin.TicketOpen {
		t.Fatalf("expected new ticket open, got %s", ticket.Status)
	}
	if ticket.SLADeadline.Before(time.Now()) {
		t.Fatal("expected SLA deadline in the future")
	}

	// Random user cannot read someone else's ticket.
	if _, _, err := svc.GetTicket(ctx, "mallory", ticket.ID); !errors.Is(err, admin.ErrNotTicketParty) {
		t.Fatalf("expected ErrNotTicketParty for a non-owner non-admin, got %v", err)
	}
	// Support can.
	if _, _, err := svc.GetTicket(ctx, "support1", ticket.ID); err != nil {
		t.Fatalf("expected support to read any ticket, got %v", err)
	}

	if _, err := svc.ReplyTicket(ctx, "support1", ticket.ID, "Looking into it"); err != nil {
		t.Fatal(err)
	}
	got, _, err := svc.GetTicket(ctx, "alice", ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != admin.TicketPending {
		t.Fatalf("expected pending after support reply, got %s", got.Status)
	}

	if _, err := svc.ReplyTicket(ctx, "alice", ticket.ID, "Still missing"); err != nil {
		t.Fatal(err)
	}
	got, _, err = svc.GetTicket(ctx, "alice", ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != admin.TicketOpen {
		t.Fatalf("expected open after user reply, got %s", got.Status)
	}

	if err := svc.ResolveTicket(ctx, "support1", ticket.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err = svc.GetTicket(ctx, "alice", ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != admin.TicketResolved {
		t.Fatalf("expected resolved, got %s", got.Status)
	}
}

func TestListTicketQueue_RequiresSupportPermission(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "ts1", admin.RoleTrustSafety); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListTicketQueue(ctx, "ts1", ""); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestGetPlatformHealth_RequiresPermission(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	if err := svc.DevGrantRole(ctx, "sup1", admin.RoleSupport); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetPlatformHealth(ctx, "sup1"); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for support viewing platform health, got %v", err)
	}
	if err := svc.DevGrantRole(ctx, "root", admin.RoleSuperAdmin); err != nil {
		t.Fatal(err)
	}
	h, err := svc.GetPlatformHealth(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected a platform health snapshot")
	}
}

func TestDevGrantRole_RejectsUnknownRole(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	err := svc.DevGrantRole(ctx, "staff1", admin.Role("wizard"))
	if !errors.Is(err, admin.ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}
}
