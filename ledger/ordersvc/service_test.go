package ordersvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/ordersvc"
	"github.com/lumena/ledger/store"
)

func timeNowPlus(hours int) time.Time {
	return time.Now().Add(time.Duration(hours) * time.Hour)
}

func newService() (*ordersvc.Service, *ledger.MemLedger) {
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	svc := ordersvc.NewService(orders, l, store.NewDevVerifier())
	return svc, l
}

func TestCreateOrder_UnknownSKURejected(t *testing.T) {
	svc, _ := newService()
	_, err := svc.CreateOrder(context.Background(), "alice", "does_not_exist", "k1")
	if !errors.Is(err, ledger.ErrUnknownSKU) {
		t.Fatalf("expected ErrUnknownSKU, got %v", err)
	}
}

func TestCreateOrder_IdempotentOnKey(t *testing.T) {
	svc, _ := newService()
	ctx := context.Background()
	o1, err := svc.CreateOrder(ctx, "alice", "coins_100", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	o2, err := svc.CreateOrder(ctx, "alice", "coins_100", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if o1.ID != o2.ID {
		t.Fatalf("expected same order on retry, got %q vs %q", o1.ID, o2.ID)
	}
}

func TestVerifyOrder_HappyPath_CreditsCoinsExactlyOnce(t *testing.T) {
	svc, l := newService()
	ctx := context.Background()
	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")

	result, err := svc.VerifyOrder(ctx, "alice", order.ID, "token-abc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ledger.OrderCredited {
		t.Fatalf("expected credited, got %s", result.Status)
	}
	if result.LedgerTransactionID == "" {
		t.Fatal("expected a ledger_transaction_id to be recorded")
	}

	bal, _ := l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 100 {
		t.Fatalf("expected balance 100, got %d", bal)
	}

	// Re-verify (client retry) must not double-credit.
	result2, err := svc.VerifyOrder(ctx, "alice", order.ID, "token-abc")
	if err != nil {
		t.Fatal(err)
	}
	if result2.Status != ledger.OrderCredited {
		t.Fatalf("expected replay to stay credited, got %s", result2.Status)
	}
	bal2, _ := l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal2 != 100 {
		t.Fatalf("expected balance still 100 after replay (no double-credit), got %d", bal2)
	}
}

func TestVerifyOrder_InvalidTokenFailsOrder(t *testing.T) {
	svc, _ := newService()
	ctx := context.Background()
	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")

	_, err := svc.VerifyOrder(ctx, "alice", order.ID, "fail-bad-token")
	if !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	got, _ := svc.GetOrder(ctx, "alice", order.ID)
	if got.Status != ledger.OrderFailed {
		t.Fatalf("expected order status failed, got %s", got.Status)
	}
}

func TestVerifyOrder_PendingLeavesOrderVisibleNotSilent(t *testing.T) {
	svc, l := newService()
	ctx := context.Background()
	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")

	result, err := svc.VerifyOrder(ctx, "alice", order.ID, "pending-token")
	if err != nil {
		t.Fatalf("expected no error while pending (status, not silence), got %v", err)
	}
	if result.Status != ledger.OrderPendingPayment {
		t.Fatalf("expected pending_payment while store hasn't confirmed, got %s", result.Status)
	}
	if result.StatusReason == "" {
		t.Fatal("expected a non-empty status reason explaining the wait")
	}
	bal, _ := l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 0 {
		t.Fatalf("expected no coins credited yet, got %d", bal)
	}
}

func TestVerifyOrder_TokenCannotBeReplayedToASecondAccount(t *testing.T) {
	svc, l := newService()
	ctx := context.Background()

	orderA, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")
	if _, err := svc.VerifyOrder(ctx, "alice", orderA.ID, "shared-token"); err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}

	orderB, _ := svc.CreateOrder(ctx, "bob", "coins_100", "k2")
	_, err := svc.VerifyOrder(ctx, "bob", orderB.ID, "shared-token")
	if !errors.Is(err, ledger.ErrTokenAlreadyUsed) {
		t.Fatalf("expected ErrTokenAlreadyUsed for replay on a second account, got %v", err)
	}

	bobBal, _ := l.Balance(ctx, ledger.UserCoinsAccount("bob"), ledger.Coin)
	if bobBal != 0 {
		t.Fatalf("expected bob to receive nothing from a replayed token, got %d", bobBal)
	}
}

func TestCreateOrder_DailyCapBlocksExcessivePurchase(t *testing.T) {
	ctx := context.Background()
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	limits := ledger.NewMemSpendLimitsRepo()
	svc := ordersvc.NewService(orders, l, store.NewDevVerifier()).WithSpendLimits(limits)

	limits.Set(ctx, &ledger.SpendLimits{AccountID: "alice", DailyCap: 150})

	// First purchase (100 coins) fits under the 150 cap.
	if _, err := svc.CreateOrder(ctx, "alice", "coins_100", "k1"); err != nil {
		t.Fatalf("expected first purchase within cap to succeed: %v", err)
	}
	// Second purchase would bring the daily total to 200, over the 150 cap.
	_, err := svc.CreateOrder(ctx, "alice", "coins_100", "k2")
	if !errors.Is(err, ledger.ErrSpendLimitExceeded) {
		t.Fatalf("expected ErrSpendLimitExceeded, got %v", err)
	}
}

func TestCreateOrder_CoolingOffBlocksAllPurchases(t *testing.T) {
	ctx := context.Background()
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	limits := ledger.NewMemSpendLimitsRepo()
	svc := ordersvc.NewService(orders, l, store.NewDevVerifier()).WithSpendLimits(limits)

	limits.Set(ctx, &ledger.SpendLimits{
		AccountID: "alice", CoolingOff: true, CoolingOffUntil: timeNowPlus(24),
	})

	_, err := svc.CreateOrder(ctx, "alice", "coins_100", "k1")
	if !errors.Is(err, ledger.ErrCoolingOff) {
		t.Fatalf("expected ErrCoolingOff, got %v", err)
	}
}

func TestVerifyOrder_WrongAccountRejected(t *testing.T) {
	svc, _ := newService()
	ctx := context.Background()
	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")

	_, err := svc.VerifyOrder(ctx, "mallory", order.ID, "token-x")
	if !errors.Is(err, ledger.ErrOrderNotOwned) {
		t.Fatalf("expected ErrOrderNotOwned, got %v", err)
	}
}
