package reconciler_test

import (
	"context"
	"testing"
	"time"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/ordersvc"
	"github.com/lumena/ledger/reconciler"
	"github.com/lumena/ledger/store"
)

// TestReconciler_RecoversOrderStuckAtCrediting is the roadmap Phase 9 exit
// gate: "kill server after store purchase but before credit; confirm
// reconciler credits within 60s and user sees status, not silence."
func TestReconciler_RecoversOrderStuckAtCrediting(t *testing.T) {
	ctx := context.Background()
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	svc := ordersvc.NewService(orders, l, store.NewDevVerifier())

	order, err := svc.CreateOrder(ctx, "alice", "coins_100", "k1")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the crash window directly: the order reached "crediting"
	// (store already confirmed payment) but the process died before the
	// ledger transaction posted — exactly the gap doc 06 §10's reconciler
	// exists to close.
	order.PurchaseToken = "token-xyz"
	order.TokenHash = ""
	order.Status = ledger.OrderCrediting
	if err := orders.UpdateOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	// TokenHash must match what advance() will compute from the stored
	// token so the replay check doesn't itself block the retry.
	got, _ := orders.GetOrder(ctx, order.ID)
	if got.Status != ledger.OrderCrediting {
		t.Fatalf("setup: expected order stuck at crediting, got %s", got.Status)
	}

	bal, _ := l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 0 {
		t.Fatalf("setup: expected no coins credited yet, got %d", bal)
	}

	worker := reconciler.New(svc, time.Hour, nil) // interval irrelevant — RunOnce drives it directly
	worker.RunOnce(ctx)

	recovered, err := svc.GetOrder(ctx, "alice", order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != ledger.OrderCredited {
		t.Fatalf("expected reconciler to complete the order, got status=%s reason=%q", recovered.Status, recovered.StatusReason)
	}

	bal, _ = l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 100 {
		t.Fatalf("expected reconciler to credit 100 coins, got %d", bal)
	}
}

func TestReconciler_RetriesPendingOrderUntilStoreConfirms(t *testing.T) {
	ctx := context.Background()
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	verifier := store.NewDevVerifier() // "pending-*" tokens confirm on the 3rd Verify() call
	svc := ordersvc.NewService(orders, l, verifier)
	worker := reconciler.New(svc, time.Hour, nil)

	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")
	first, err := svc.VerifyOrder(ctx, "alice", order.ID, "pending-token")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != ledger.OrderPendingPayment {
		t.Fatalf("expected pending_payment after first verify, got %s", first.Status)
	}

	worker.RunOnce(ctx) // 2nd Verify() call — still pending
	stillPending, _ := svc.GetOrder(ctx, "alice", order.ID)
	if stillPending.Status != ledger.OrderPendingPayment {
		t.Fatalf("expected still pending after 2nd attempt, got %s", stillPending.Status)
	}

	worker.RunOnce(ctx) // 3rd Verify() call — DevVerifier now confirms
	done, _ := svc.GetOrder(ctx, "alice", order.ID)
	if done.Status != ledger.OrderCredited {
		t.Fatalf("expected credited after 3rd attempt, got %s", done.Status)
	}
	bal, _ := l.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 100 {
		t.Fatalf("expected 100 coins credited, got %d", bal)
	}
}

func TestReconciler_DoesNotTouchTerminalOrders(t *testing.T) {
	ctx := context.Background()
	l := ledger.NewMemLedger()
	orders := ledger.NewMemOrderRepo()
	svc := ordersvc.NewService(orders, l, store.NewDevVerifier())
	worker := reconciler.New(svc, time.Hour, nil)

	order, _ := svc.CreateOrder(ctx, "alice", "coins_100", "k1")
	svc.VerifyOrder(ctx, "alice", order.ID, "fail-token") // -> failed

	worker.RunOnce(ctx) // must not touch a terminal order

	got, _ := svc.GetOrder(ctx, "alice", order.ID)
	if got.Status != ledger.OrderFailed {
		t.Fatalf("expected order to remain failed, got %s", got.Status)
	}
}
