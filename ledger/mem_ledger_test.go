package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumena/ledger"
)

func seedCoins(t *testing.T, l *ledger.MemLedger, accountID string, amount int64) {
	t.Helper()
	_, _, err := l.PostTransaction(context.Background(), "promo", "seed-"+accountID, nil, []ledger.Entry{
		{AccountID: accountID, Amount: amount, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -amount, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestPostTransaction_BalancedEntriesApply(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 1000)

	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal != 1000 {
		t.Fatalf("expected balance 1000, got %d", bal)
	}
}

func TestPostTransaction_UnbalancedRejected(t *testing.T) {
	l := ledger.NewMemLedger()
	_, _, err := l.PostTransaction(context.Background(), "gift", "k1", nil, []ledger.Entry{
		{AccountID: "a", Amount: -100, Currency: ledger.Coin},
		{AccountID: "b", Amount: 90, Currency: ledger.Coin}, // does not sum to zero
	})
	if !errors.Is(err, ledger.ErrUnbalancedTransaction) {
		t.Fatalf("expected ErrUnbalancedTransaction, got %v", err)
	}
}

func TestPostTransaction_IdempotentOnKey(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 1000)
	entries := []ledger.Entry{
		{AccountID: acct, Amount: -100, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 100, Currency: ledger.Coin},
	}
	tx1, isNew1, err := l.PostTransaction(ctx, "gift", "dup-key", nil, entries)
	if err != nil || !isNew1 {
		t.Fatalf("expected new tx, got isNew=%v err=%v", isNew1, err)
	}
	tx2, isNew2, err := l.PostTransaction(ctx, "gift", "dup-key", nil, entries)
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 {
		t.Fatal("expected retry with same idempotency key to not post again")
	}
	if tx1.ID != tx2.ID {
		t.Fatalf("expected same transaction returned, got %q vs %q", tx1.ID, tx2.ID)
	}

	// Balance should reflect exactly one application, not two.
	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal != 900 {
		t.Fatalf("expected balance 900 (1000-100) after single application, got %d (double-spend if 800)", bal)
	}
}

func TestPostTransaction_InsufficientBalanceAppliesNothing(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 100)

	_, _, err := l.PostTransaction(ctx, "gift", "overspend", nil, []ledger.Entry{
		{AccountID: acct, Amount: -500, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 500, Currency: ledger.Coin},
	})
	var insufficient *ledger.InsufficientBalanceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("expected InsufficientBalanceError, got %v", err)
	}
	if insufficient.Shortfall() != 400 {
		t.Fatalf("expected shortfall 400 (need 500, have 100), got %d", insufficient.Shortfall())
	}

	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal != 100 {
		t.Fatalf("expected balance unchanged at 100 after rejected transaction, got %d", bal)
	}
	revenue, _ := l.Balance(ctx, ledger.PlatformRevenueAccount, ledger.Coin)
	if revenue != 0 {
		t.Fatalf("expected platform revenue unchanged (all-or-nothing), got %d", revenue)
	}
}

func TestBalance_NeverGoesNegativeUnderConcurrentAttempts(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 100)

	// Sequential is sufficient to prove the invariant given the mutex design;
	// a race-detector run (go test -race) exercises the concurrency path.
	successes := 0
	for i := 0; i < 5; i++ {
		_, _, err := l.PostTransaction(ctx, "gift", "attempt-"+itoaTest(i), nil, []ledger.Entry{
			{AccountID: acct, Amount: -30, Currency: ledger.Coin},
			{AccountID: ledger.PlatformRevenueAccount, Amount: 30, Currency: ledger.Coin},
		})
		if err == nil {
			successes++
		}
	}
	if successes != 3 {
		t.Fatalf("expected exactly 3 of 5 debits of 30 against balance 100 to succeed, got %d", successes)
	}
	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal < 0 {
		t.Fatalf("balance went negative: %d", bal)
	}
}

func TestReverseTransaction_RestoresBalanceViaCompensatingEntry(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 1000)

	tx, _, err := l.PostTransaction(ctx, "gift", "gift-1", map[string]any{"gift_id": "g_rose"}, []ledger.Entry{
		{AccountID: acct, Amount: -100, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 100, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatal(err)
	}
	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal != 900 {
		t.Fatalf("expected 900 after the gift, got %d", bal)
	}

	reversal, isNew, err := l.ReverseTransaction(ctx, tx.ID, "reverse-1", "customer dispute")
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || reversal.Kind != "reversal" {
		t.Fatalf("unexpected reversal: isNew=%v %+v", isNew, reversal)
	}
	if reversal.Metadata["reversed_transaction_id"] != tx.ID {
		t.Fatalf("expected reversal metadata to reference the original tx, got %+v", reversal.Metadata)
	}

	bal, _ = l.Balance(ctx, acct, ledger.Coin)
	if bal != 1000 {
		t.Fatalf("expected balance restored to 1000 after reversal, got %d", bal)
	}

	// The original transaction is still there, unmodified — doc rule 15:
	// "never destroy financial history".
	original, err := l.GetTransactionByID(ctx, tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if original.Kind != "gift" || len(original.Entries) != 2 {
		t.Fatalf("original transaction was mutated: %+v", original)
	}

	// Retrying the same idempotency key doesn't reverse it twice.
	reversal2, isNew2, err := l.ReverseTransaction(ctx, tx.ID, "reverse-1", "customer dispute")
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 || reversal2.ID != reversal.ID {
		t.Fatalf("expected idempotent replay to return the same reversal, got isNew=%v id=%s", isNew2, reversal2.ID)
	}

	// A different idempotency key against an already-reversed transaction is rejected.
	if _, _, err := l.ReverseTransaction(ctx, tx.ID, "reverse-2", "again"); !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("expected ErrAlreadyReversed, got %v", err)
	}
}

func TestReverseTransaction_UnknownIDRejected(t *testing.T) {
	l := ledger.NewMemLedger()
	_, _, err := l.ReverseTransaction(context.Background(), "tx-does-not-exist", "k1", "reason")
	if !errors.Is(err, ledger.ErrTransactionNotFound) {
		t.Fatalf("expected ErrTransactionNotFound, got %v", err)
	}
}

func TestReverseTransaction_CannotReverseAReversal(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 1000)

	tx, _, err := l.PostTransaction(ctx, "gift", "gift-1", nil, []ledger.Entry{
		{AccountID: acct, Amount: -100, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 100, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatal(err)
	}
	reversal, _, err := l.ReverseTransaction(ctx, tx.ID, "reverse-1", "dispute")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.ReverseTransaction(ctx, reversal.ID, "reverse-of-reverse", "oops"); !errors.Is(err, ledger.ErrCannotReverseAReversal) {
		t.Fatalf("expected ErrCannotReverseAReversal, got %v", err)
	}
}

func TestReverseTransaction_InsufficientBalanceAppliesNothing(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 100)

	// Reverse a transaction where alice's coins were credited (a purchase)
	// after she's already spent them — reversing the credit (debiting her
	// again) must fail rather than drive her balance negative.
	tx2, _, err := l.PostTransaction(ctx, "purchase", "purchase-1", nil, []ledger.Entry{
		{AccountID: acct, Amount: 50, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -50, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatal(err)
	}
	// alice now has 100 + 50 = 150; spend most of it, leaving less than the
	// 50 a reversal of the purchase would need to claw back.
	if _, _, err := l.PostTransaction(ctx, "gift", "gift-2", nil, []ledger.Entry{
		{AccountID: acct, Amount: -140, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 140, Currency: ledger.Coin},
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err = l.ReverseTransaction(ctx, tx2.ID, "reverse-purchase", "refund")
	var insufficient *ledger.InsufficientBalanceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("expected InsufficientBalanceError reversing a purchase whose coins were already spent, got %v", err)
	}

	// Nothing applied: alice's balance is unchanged at 10 (150-140).
	bal, _ := l.Balance(ctx, acct, ledger.Coin)
	if bal != 10 {
		t.Fatalf("expected balance unchanged at 10 after the rejected reversal, got %d", bal)
	}
}

func TestIntegrityCheck_SumsToZeroAcrossWholeLedgerAfterMixedActivity(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	seedCoins(t, l, ledger.UserCoinsAccount("alice"), 1000)
	seedCoins(t, l, ledger.UserCoinsAccount("bob"), 500)
	l.PostTransaction(ctx, "gift", "g1", nil, []ledger.Entry{
		{AccountID: ledger.UserCoinsAccount("alice"), Amount: -300, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 90, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 210, Currency: ledger.Coin},
		{AccountID: ledger.UserDiamondsAccount("bob"), Amount: 147, Currency: ledger.Diamond},
		{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -147, Currency: ledger.Diamond},
	})

	sums := l.IntegrityCheck()
	for currency, sum := range sums {
		if sum != 0 {
			t.Errorf("expected %s to sum to zero across the whole ledger, got %d", currency, sum)
		}
	}
}

func TestHistory_ReturnsEntriesForAccountMostRecentFirst(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, acct, 1000)
	l.PostTransaction(ctx, "gift", "g1", nil, []ledger.Entry{
		{AccountID: acct, Amount: -50, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: 50, Currency: ledger.Coin},
	})

	items, _, err := l.History(ctx, acct, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 history items (seed + gift), got %d", len(items))
	}
	if items[0].Amount != -50 {
		t.Fatalf("expected most recent entry (-50) first, got %+v", items[0])
	}
}

func itoaTest(n int) string {
	return string(rune('0' + n))
}
