package ledger_test

// Real concurrency tests (roadmap Phase 19: Performance Testing) — actual
// goroutines racing the real mutex-guarded MemLedger, meant to be run with
// `go test -race` so the race detector, not just the assertions, is the
// thing actually proving safety. Scaled to what a single dev process can
// meaningfully exercise (hundreds of goroutines) — see the package doc
// comment on why doc 09 §8's production targets (500 concurrent
// broadcasts, 50k viewers, distributed Kafka/Postgres chaos scenarios)
// aren't reproducible against this in-memory, single-process build.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lumena/ledger"
)

// TestConcurrentIdempotentRetries_ExactlyOnePosts fires the same
// idempotency key from 500 concurrent goroutines — a real-world retry
// storm (client timeout + naive retry, or a duplicated request from a
// flaky mobile network). Exactly one must actually post; every other
// caller must get back the same transaction, not a second charge.
func TestConcurrentIdempotentRetries_ExactlyOnePosts(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()
	sender := ledger.UserCoinsAccount("alice")
	seedCoins(t, l, sender, 1_000_000)

	const n = 500
	var wg sync.WaitGroup
	txIDs := make([]string, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, _, err := l.PostTransaction(ctx, "gift", "race-key-1", nil, []ledger.Entry{
				{AccountID: sender, Amount: -10, Currency: ledger.Coin},
				{AccountID: ledger.PlatformRevenueAccount, Amount: 10, Currency: ledger.Coin},
			})
			errs[i] = err
			if tx != nil {
				txIDs[i] = tx.ID
			}
		}(i)
	}
	wg.Wait()

	firstID := ""
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d got unexpected error: %v", i, errs[i])
		}
		if firstID == "" {
			firstID = txIDs[i]
		}
		if txIDs[i] != firstID {
			t.Fatalf("goroutine %d got a different transaction id (%s) than goroutine 0 (%s) for the same idempotency key", i, txIDs[i], firstID)
		}
	}

	bal, err := l.Balance(ctx, sender, ledger.Coin)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 1_000_000-10 {
		t.Fatalf("expected exactly one 10-coin debit to have applied despite %d concurrent callers, got balance %d", n, bal)
	}
}

// TestConcurrentDistinctTransactions_IntegrityHoldsAtScale posts 500
// distinct concurrent gift-shaped transactions (each its own idempotency
// key, i.e. 500 different simultaneous gift sends) and asserts the
// whole-ledger zero-sum invariant (doc 06 §9) still holds afterward — the
// dev-scale analog of doc 09 §8's "500 concurrent broadcasts" load
// target, applied to the money path instead (this build has no ingest
// nodes to load-test broadcasts against).
func TestConcurrentDistinctTransactions_IntegrityHoldsAtScale(t *testing.T) {
	l := ledger.NewMemLedger()
	ctx := context.Background()

	const n = 500
	seedCoins(t, l, ledger.UserCoinsAccount("whale"), int64(n)*100)

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recipient := ledger.UserDiamondsAccount(fmt.Sprintf("creator-%d", i%25)) // 25 distinct recipients, contended
			_, _, err := l.PostTransaction(ctx, "gift", fmt.Sprintf("concurrent-key-%d", i), nil, []ledger.Entry{
				{AccountID: ledger.UserCoinsAccount("whale"), Amount: -100, Currency: ledger.Coin},
				{AccountID: ledger.PlatformRevenueAccount, Amount: 30, Currency: ledger.Coin},
				{AccountID: ledger.PlatformLiabilityAccount, Amount: 70, Currency: ledger.Coin},
				{AccountID: recipient, Amount: 49, Currency: ledger.Diamond},
				{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -49, Currency: ledger.Diamond},
			})
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("unexpected error under concurrent load: %v", err)
		}
	}

	sums := l.IntegrityCheck()
	for currency, sum := range sums {
		if sum != 0 {
			t.Fatalf("ledger integrity violated after %d concurrent transactions: %s sums to %d, want 0", n, currency, sum)
		}
	}

	whaleBal, _ := l.Balance(ctx, ledger.UserCoinsAccount("whale"), ledger.Coin)
	if whaleBal != 0 {
		t.Fatalf("expected whale's coin balance fully spent (%d x 100), got %d remaining", n, whaleBal)
	}
}
