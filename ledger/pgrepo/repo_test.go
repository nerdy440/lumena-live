package pgrepo_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/ledger"
	"github.com/lumena/ledger/pgrepo"
)

func newTestRepo(t *testing.T) *pgrepo.Repo {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.New(pool)
}

func seedCoins(t *testing.T, r *pgrepo.Repo, accountID string, amount int64) {
	t.Helper()
	_, _, err := r.PostTransaction(context.Background(), "promo", "seed-"+accountID, nil, []ledger.Entry{
		{AccountID: accountID, Amount: amount, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -amount, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestListByKind_FiltersByKindAndSince(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, r, acct, 1000) // kind="promo"

	cutoff := time.Now()
	_, _, err := r.PostTransaction(ctx, "gift", "gift-1", map[string]any{"sender_id": "alice"}, []ledger.Entry{
		{AccountID: acct, Amount: -50, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 50, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("post gift: %v", err)
	}

	gifts, err := r.ListByKind(ctx, "gift", cutoff)
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if len(gifts) != 1 || gifts[0].IdempotencyKey != "gift-1" || len(gifts[0].Entries) != 2 {
		t.Fatalf("unexpected gifts: %+v", gifts)
	}
	if gifts[0].Metadata["sender_id"] != "alice" {
		t.Fatalf("metadata not round-tripped: %+v", gifts[0].Metadata)
	}

	promos, err := r.ListByKind(ctx, "promo", cutoff)
	if err != nil || len(promos) != 0 {
		t.Fatalf("expected no promo txns after cutoff, got %+v err=%v", promos, err)
	}
}

func TestIntegrityCheck_SumsToZeroAcrossBalancedTransactions(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	seedCoins(t, r, ledger.UserCoinsAccount("alice"), 500)
	seedCoins(t, r, ledger.UserCoinsAccount("bob"), 300)
	_, _, err := r.PostTransaction(ctx, "gift", "gift-integrity", nil, []ledger.Entry{
		{AccountID: ledger.UserCoinsAccount("alice"), Amount: -100, Currency: ledger.Coin},
		{AccountID: ledger.UserDiamondsAccount("bob"), Amount: 70, Currency: ledger.Diamond},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 100, Currency: ledger.Coin},
		{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -70, Currency: ledger.Diamond},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	sums := r.IntegrityCheck()
	for currency, sum := range sums {
		if sum != 0 {
			t.Fatalf("currency %s summed to %d, want 0: %+v", currency, sum, sums)
		}
	}
}

func TestPostTransaction_BalancedEntriesApplyAndBalanceReads(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, r, acct, 1000)

	bal, err := r.Balance(ctx, acct, ledger.Coin)
	if err != nil || bal != 1000 {
		t.Fatalf("balance = %d, err = %v, want 1000", bal, err)
	}
}

func TestPostTransaction_UnbalancedRejected(t *testing.T) {
	r := newTestRepo(t)
	_, _, err := r.PostTransaction(context.Background(), "gift", "k1", nil, []ledger.Entry{
		{AccountID: "a", Amount: -100, Currency: ledger.Coin},
		{AccountID: "b", Amount: 90, Currency: ledger.Coin},
	})
	if !errors.Is(err, ledger.ErrUnbalancedTransaction) {
		t.Fatalf("expected ErrUnbalancedTransaction, got %v", err)
	}
}

func TestPostTransaction_IdempotentOnKey(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("bob")
	entries := []ledger.Entry{
		{AccountID: acct, Amount: 50, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -50, Currency: ledger.Coin},
	}
	tx1, isNew1, err := r.PostTransaction(ctx, "promo", "idem-1", nil, entries)
	if err != nil || !isNew1 {
		t.Fatalf("first post: tx=%+v isNew=%v err=%v", tx1, isNew1, err)
	}
	tx2, isNew2, err := r.PostTransaction(ctx, "promo", "idem-1", nil, entries)
	if err != nil || isNew2 {
		t.Fatalf("retry: expected isNew=false, got tx=%+v isNew=%v err=%v", tx2, isNew2, err)
	}
	if tx1.ID != tx2.ID {
		t.Fatalf("retry returned a different transaction: %s vs %s", tx1.ID, tx2.ID)
	}
	bal, _ := r.Balance(ctx, acct, ledger.Coin)
	if bal != 50 {
		t.Fatalf("balance = %d after retried post, want 50 (must not double-apply)", bal)
	}
}

func TestPostTransaction_InsufficientBalanceAppliesNothing(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("carol")
	seedCoins(t, r, acct, 100)

	_, _, err := r.PostTransaction(ctx, "gift", "spend-1", nil, []ledger.Entry{
		{AccountID: acct, Amount: -500, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 500, Currency: ledger.Coin},
	})
	var insufficient *ledger.InsufficientBalanceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("expected InsufficientBalanceError, got %v", err)
	}
	bal, _ := r.Balance(ctx, acct, ledger.Coin)
	if bal != 100 {
		t.Fatalf("balance = %d after rejected spend, want unchanged 100", bal)
	}
}

func TestReverseTransaction_RestoresBalance(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("dave")
	seedCoins(t, r, acct, 1000)

	spendTx, _, err := r.PostTransaction(ctx, "gift", "spend-2", nil, []ledger.Entry{
		{AccountID: acct, Amount: -300, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 300, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	_, isNew, err := r.ReverseTransaction(ctx, spendTx.ID, "reverse-1", "refund")
	if err != nil || !isNew {
		t.Fatalf("reverse: isNew=%v err=%v", isNew, err)
	}
	bal, _ := r.Balance(ctx, acct, ledger.Coin)
	if bal != 1000 {
		t.Fatalf("balance after reversal = %d, want restored 1000", bal)
	}

	// Cannot reverse a reversal, cannot reverse the same tx twice.
	_, _, err = r.ReverseTransaction(ctx, spendTx.ID, "reverse-2", "again")
	if !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("expected ErrAlreadyReversed, got %v", err)
	}
}

func TestHistory_MostRecentFirstWithPagination(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("erin")
	seedCoins(t, r, acct, 300) // #1
	_, _, _ = r.PostTransaction(ctx, "gift", "h2", nil, []ledger.Entry{
		{AccountID: acct, Amount: -10, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 10, Currency: ledger.Coin},
	}) // #2
	_, _, _ = r.PostTransaction(ctx, "gift", "h3", nil, []ledger.Entry{
		{AccountID: acct, Amount: -10, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 10, Currency: ledger.Coin},
	}) // #3

	page, cursor, err := r.History(ctx, acct, "", 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page) != 2 || page[0].Kind != "gift" {
		t.Fatalf("expected most recent 2 items first, got %+v", page)
	}
	if cursor == "" {
		t.Fatal("expected a next cursor since a third item remains")
	}
	rest, cursor2, err := r.History(ctx, acct, cursor, 2)
	if err != nil {
		t.Fatalf("History page 2: %v", err)
	}
	if len(rest) != 1 || rest[0].Kind != "promo" {
		t.Fatalf("expected the seed transaction on page 2, got %+v", rest)
	}
	if cursor2 != "" {
		t.Fatalf("expected no further cursor, got %q", cursor2)
	}
}

// TestPostTransaction_ConcurrentSpendsNeverGoNegative proves the
// SERIALIZABLE-isolation + retry strategy actually holds under real
// concurrent load against Postgres — many goroutines racing to spend
// from an account with only enough balance for a handful of them must
// result in exactly that many successes, and the final balance must
// never go negative. This is the real-database analogue of
// mem_ledger's TestBalance_NeverGoesNegativeUnderConcurrentAttempts.
func TestPostTransaction_ConcurrentSpendsNeverGoNegative(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	acct := ledger.UserCoinsAccount("frank")
	seedCoins(t, r, acct, 500) // enough for exactly 5 spends of 100

	const attempts = 20
	var wg sync.WaitGroup
	var successes int32
	var mu sync.Mutex
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := r.PostTransaction(ctx, "gift", fmt.Sprintf("concurrent-spend-%d", i), nil, []ledger.Entry{
				{AccountID: acct, Amount: -100, Currency: ledger.Coin},
				{AccountID: ledger.PlatformLiabilityAccount, Amount: 100, Currency: ledger.Coin},
			})
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if successes != 5 {
		t.Fatalf("successful concurrent spends = %d, want exactly 5 (500 balance / 100 each)", successes)
	}
	bal, err := r.Balance(ctx, acct, ledger.Coin)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("final balance = %d, want exactly 0 (never negative, never left with un-spent leftover)", bal)
	}
}

