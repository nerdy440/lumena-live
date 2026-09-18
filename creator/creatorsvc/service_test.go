package creatorsvc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lumena/creator"
	"github.com/lumena/creator/creatorsvc"
)

// fakeLedger is an in-memory stand-in for the real ledger, scoped to one
// account's diamonds history — enough to exercise the earnings/payout math
// without pulling in the ledger module.
type fakeLedger struct {
	entries  []creator.EarningEntry
	balance  int64
	posted   []int64 // amounts debited via postPayout, in call order
	reversed []int64 // amounts credited back via reversePayout, in call order
	failNext bool
}

func (f *fakeLedger) history(_ context.Context, _ string) ([]creator.EarningEntry, error) {
	return f.entries, nil
}
func (f *fakeLedger) getBalance(_ context.Context, _ string) (int64, error) {
	return f.balance, nil
}
func (f *fakeLedger) postPayout(_ context.Context, _ string, amount int64, _ string) (string, error) {
	if f.failNext {
		return "", errors.New("simulated ledger failure")
	}
	f.balance -= amount
	f.entries = append(f.entries, creator.EarningEntry{
		TransactionID: "payout-tx", Kind: "payout", AmountDiamonds: -amount, CreatedAt: time.Now(),
	})
	f.posted = append(f.posted, amount)
	return "ledger-tx-1", nil
}

func (f *fakeLedger) reversePayout(_ context.Context, _ string, amount int64, _ string) (string, error) {
	f.balance += amount
	f.entries = append(f.entries, creator.EarningEntry{
		TransactionID: "payout-reversal-tx", Kind: "payout_reversal", AmountDiamonds: amount, CreatedAt: time.Now(),
	})
	f.reversed = append(f.reversed, amount)
	return "ledger-reversal-tx-1", nil
}

func notRestricted(context.Context, string) (bool, error) { return false, nil }
func restricted(context.Context, string) (bool, error)    { return true, nil }
func noSessions(context.Context, string) ([]creator.StreamStat, error) {
	return nil, nil
}
func noFollowers(context.Context, string) ([]creator.FollowerPoint, error) {
	return nil, nil
}

func newTestService(fl *fakeLedger, acctRestricted creatorsvc.AccountRestrictedFunc) *creatorsvc.Service {
	return creatorsvc.New(
		creator.NewMemKYCRepo(),
		creator.NewMemPayoutRepo(),
		creator.NewMemPayoutProfileRepo(),
		fl.history,
		fl.getBalance,
		acctRestricted,
		fl.postPayout,
		fl.reversePayout,
		noSessions,
		noFollowers,
	)
}

// setPayoutProfile satisfies the payout-profile gate added to RequestPayout
// — tests that only mean to exercise KYC/threshold/restriction/amount
// checks call this right after DevApproveKYC so the profile gate itself
// doesn't mask the thing under test.
func setPayoutProfile(t *testing.T, svc *creatorsvc.Service, accountID string) {
	t.Helper()
	if _, err := svc.SetPayoutProfile(context.Background(), accountID, creator.PayoutMethodBankTransfer, "PK", "Test Creator", "PK00TESTBANK0000000000000000"); err != nil {
		t.Fatalf("setPayoutProfile: %v", err)
	}
}

func TestEarningsSummary_UnlockedOnlyAfterHoldPeriod(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	recent := time.Now().Add(-2 * 24 * time.Hour)
	fl := &fakeLedger{
		balance: 15000,
		entries: []creator.EarningEntry{
			{TransactionID: "t1", Kind: "gift", GiftID: "rose", AmountDiamonds: 10000, CreatedAt: old},
			{TransactionID: "t2", Kind: "pv_settle", AmountDiamonds: 5000, CreatedAt: recent},
		},
	}
	svc := newTestService(fl, notRestricted)

	summary, err := svc.GetEarningsSummary(ctx, "creator1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalEarnedDiamonds != 15000 {
		t.Fatalf("expected total 15000, got %d", summary.TotalEarnedDiamonds)
	}
	if summary.AvailableDiamonds != 10000 {
		t.Fatalf("expected available 10000 (only the >14d entry), got %d", summary.AvailableDiamonds)
	}
	if summary.LockedDiamonds != 5000 {
		t.Fatalf("expected locked 5000, got %d", summary.LockedDiamonds)
	}
	if len(summary.ByGift) != 1 || summary.ByGift[0].GiftID != "rose" || summary.ByGift[0].TotalDiamonds != 10000 {
		t.Fatalf("unexpected gift breakdown: %+v", summary.ByGift)
	}
}

func TestRequestPayout_BlockedWithoutKYC(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)

	_, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if !errors.Is(err, creator.ErrKYCIncomplete) {
		t.Fatalf("expected ErrKYCIncomplete, got %v", err)
	}
}

func TestRequestPayout_BlockedDuringHoldPeriod(t *testing.T) {
	ctx := context.Background()
	recent := time.Now().Add(-1 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: recent},
	}}
	svc := newTestService(fl, notRestricted)

	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	_, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if !errors.Is(err, creator.ErrBelowThreshold) {
		t.Fatalf("expected ErrBelowThreshold (all earnings still locked), got %v", err)
	}
}

func TestSetPayoutProfile_RejectsUnsupportedCountry(t *testing.T) {
	ctx := context.Background()
	fl := &fakeLedger{}
	svc := newTestService(fl, notRestricted)

	if _, err := svc.SetPayoutProfile(ctx, "creator1", creator.PayoutMethodBankTransfer, "ZZ", "Someone", "12345"); !errors.Is(err, creator.ErrUnsupportedPayoutCountry) {
		t.Fatalf("expected ErrUnsupportedPayoutCountry for an unknown country code, got %v", err)
	}
}

func TestSetPayoutProfile_RejectsMethodNotAvailableForCountry(t *testing.T) {
	ctx := context.Background()
	fl := &fakeLedger{}
	svc := newTestService(fl, notRestricted)

	// PK's catalogue entry offers bank_transfer and mobile_wallet, not paypal.
	if _, err := svc.SetPayoutProfile(ctx, "creator1", creator.PayoutMethodPayPal, "PK", "Someone", "someone@example.com"); !errors.Is(err, creator.ErrUnsupportedPayoutMethod) {
		t.Fatalf("expected ErrUnsupportedPayoutMethod for paypal in PK, got %v", err)
	}
}

func TestSetPayoutProfile_AcceptsSupportedCountryAndMethod(t *testing.T) {
	ctx := context.Background()
	fl := &fakeLedger{}
	svc := newTestService(fl, notRestricted)

	p, err := svc.SetPayoutProfile(ctx, "creator1", creator.PayoutMethodMobileWallet, "PK", "Someone", "03001234567")
	if err != nil {
		t.Fatal(err)
	}
	if p.Country != "PK" || p.Method != creator.PayoutMethodMobileWallet {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

func TestRequestPayout_BlockedWithoutPayoutProfile(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)

	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if !errors.Is(err, creator.ErrPayoutProfileRequired) {
		t.Fatalf("expected ErrPayoutProfileRequired, got %v", err)
	}
}

func TestRequestPayout_BlockedBelowThreshold(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 5000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 5000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)

	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	_, err := svc.RequestPayout(ctx, "creator1", 5000, "idem-1")
	if !errors.Is(err, creator.ErrBelowThreshold) {
		t.Fatalf("expected ErrBelowThreshold (5000 < 10000 threshold), got %v", err)
	}
}

func TestRequestPayout_BlockedWhenAccountRestricted(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, restricted)

	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	_, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if !errors.Is(err, creator.ErrAccountRestricted) {
		t.Fatalf("expected ErrAccountRestricted, got %v", err)
	}
}

func TestRequestPayout_SucceedsAndPostsLedgerTransaction(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)

	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	p, err := svc.RequestPayout(ctx, "creator1", 12000, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != creator.PayoutRequested {
		t.Fatalf("expected initial status requested, got %s", p.Status)
	}
	if len(fl.posted) != 1 || fl.posted[0] != 12000 {
		t.Fatalf("expected ledger posted [12000], got %v", fl.posted)
	}

	// Requesting more than the (now reduced) available balance fails.
	_, err = svc.RequestPayout(ctx, "creator1", 12000, "idem-2")
	if !errors.Is(err, creator.ErrInvalidAmount) && !errors.Is(err, creator.ErrBelowThreshold) {
		t.Fatalf("expected a rejection for over-requesting, got %v", err)
	}
}

func TestRequestPayout_RequiresIdempotencyDoesNotDoublePostOnRetry(t *testing.T) {
	// The idempotency contract itself lives in the ledger module (tested
	// there); here we only verify creatorsvc passes the key through
	// unmodified so a real ledger's dedup can do its job.
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	var seenKeys []string
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	postAndCapture := func(ctx context.Context, accountID string, amount int64, key string) (string, error) {
		seenKeys = append(seenKeys, key)
		return fl.postPayout(ctx, accountID, amount, key)
	}
	svc := creatorsvc.New(
		creator.NewMemKYCRepo(), creator.NewMemPayoutRepo(), creator.NewMemPayoutProfileRepo(),
		fl.history, fl.getBalance, notRestricted, postAndCapture, fl.reversePayout, noSessions, noFollowers,
	)
	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	if _, err := svc.RequestPayout(ctx, "creator1", 10000, "my-idem-key"); err != nil {
		t.Fatal(err)
	}
	if len(seenKeys) != 1 || seenKeys[0] != "my-idem-key" {
		t.Fatalf("expected idempotency key passed through, got %v", seenKeys)
	}
}

// TestPayoutNeverAutoAdvances replaces the old "eventually completes on a
// timer" behavior: a payout must sit in Requested until an admin explicitly
// acts on it — there is no real payment rail in this dev build to hand it
// off to, so silently auto-completing would be exactly the kind of fake
// financial functionality doc rule 8 forbids.
func TestPayoutNeverAutoAdvances(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)
	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	p, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)
	got, err := svc.GetPayout(ctx, "creator1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != creator.PayoutRequested {
		t.Fatalf("expected status to stay requested with no admin action, got %s", got.Status)
	}
}

// TestAdminPayoutHappyPath exercises the full manual-payout MVP state
// machine: requested -> approved -> processing -> paid, with reviewer and
// payout_reference recorded at each admin step (doc rule 18).
func TestAdminPayoutHappyPath(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)
	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	p, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if err != nil {
		t.Fatal(err)
	}

	approved, err := svc.AdminApprovePayout(ctx, "admin1", p.ID, "looks good")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != creator.PayoutApproved || approved.ReviewedBy != "admin1" {
		t.Fatalf("unexpected approved payout: %+v", approved)
	}

	// Can't mark paid before processing.
	if _, err := svc.AdminMarkPaid(ctx, "admin1", p.ID, "ref-123"); !errors.Is(err, creator.ErrInvalidPayoutTransition) {
		t.Fatalf("expected ErrInvalidPayoutTransition marking paid before processing, got %v", err)
	}

	processing, err := svc.AdminMarkProcessing(ctx, "admin1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != creator.PayoutProcessing {
		t.Fatalf("expected processing, got %s", processing.Status)
	}

	paid, err := svc.AdminMarkPaid(ctx, "admin1", p.ID, "ref-123")
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != creator.PayoutPaid || paid.PayoutReference != "ref-123" || paid.PaidAt == nil {
		t.Fatalf("unexpected paid payout: %+v", paid)
	}
	if len(fl.reversed) != 0 {
		t.Fatalf("a successfully paid payout must never be reversed, got %v", fl.reversed)
	}
}

// TestAdminRejectPayout_ReversesReservedBalance verifies doc rule 19: a
// rejected payout's held amount returns to available via a real ledger
// reversal, not a direct balance edit.
func TestAdminRejectPayout_ReversesReservedBalance(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-20 * 24 * time.Hour)
	fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
		{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
	}}
	svc := newTestService(fl, notRestricted)
	if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
		t.Fatal(err)
	}
	setPayoutProfile(t, svc, "creator1")
	p, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
	if err != nil {
		t.Fatal(err)
	}

	rejected, err := svc.AdminRejectPayout(ctx, "admin1", p.ID, "payout profile could not be verified")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != creator.PayoutRejected || rejected.ReversalTransactionID == "" {
		t.Fatalf("unexpected rejected payout: %+v", rejected)
	}
	if len(fl.reversed) != 1 || fl.reversed[0] != 10000 {
		t.Fatalf("expected a 10000-diamond reversal, got %v", fl.reversed)
	}
	if fl.balance != 20000 {
		t.Fatalf("expected balance restored to 20000 after reversal, got %d", fl.balance)
	}

	// A terminal payout can't be rejected again.
	if _, err := svc.AdminRejectPayout(ctx, "admin1", p.ID, "again"); !errors.Is(err, creator.ErrInvalidPayoutTransition) {
		t.Fatalf("expected ErrInvalidPayoutTransition re-rejecting a terminal payout, got %v", err)
	}
}

// TestConcurrentRejectAndMarkProcessing_NeverLeavesLedgerAheadOfStatus is a
// regression test for a real race: rejectOrFail used to call the external
// ledger reversal, THEN separately re-check the payout's status inside
// Update — so a concurrent AdminMarkProcessing could land between those two
// steps, moving the payout to Processing while the reversal (a real ledger
// transaction returning diamonds to "available") had already been posted.
// That left the ledger showing the money back while the payout pressed
// forward toward a real payment — a double-payment channel. The fix moved
// the status re-check and the reversal call into the SAME Update critical
// section. This test races the two calls many times and asserts the
// invariant that always must hold: a reversal transaction exists if and
// only if the payout actually ended up Rejected.
func TestConcurrentRejectAndMarkProcessing_NeverLeavesLedgerAheadOfStatus(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx := context.Background()
		old := time.Now().Add(-20 * 24 * time.Hour)
		fl := &fakeLedger{balance: 20000, entries: []creator.EarningEntry{
			{Kind: "gift", AmountDiamonds: 20000, CreatedAt: old},
		}}
		svc := newTestService(fl, notRestricted)
		if _, err := svc.DevApproveKYC(ctx, "creator1"); err != nil {
			t.Fatal(err)
		}
		setPayoutProfile(t, svc, "creator1")
		p, err := svc.RequestPayout(ctx, "creator1", 10000, "idem-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.AdminApprovePayout(ctx, "admin1", p.ID, ""); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = svc.AdminRejectPayout(ctx, "admin1", p.ID, "race") }()
		go func() { defer wg.Done(); _, _ = svc.AdminMarkProcessing(ctx, "admin1", p.ID) }()
		wg.Wait()

		final, err := svc.GetPayout(ctx, "creator1", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		reversed := len(fl.reversed) > 0
		if reversed != (final.Status == creator.PayoutRejected) {
			t.Fatalf("iteration %d: ledger reversed=%v but final status=%s — these must always agree", i, reversed, final.Status)
		}
	}
}
