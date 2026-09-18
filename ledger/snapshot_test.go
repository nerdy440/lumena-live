package ledger_test

import (
	"context"
	"testing"

	"github.com/lumena/ledger"
)

// TestSnapshotRestore_RoundTripsBalancesAndHistory proves the local-
// persistence-to-disk feature (feed/cmd/api's localstate.go) actually
// recovers usable state, not just bytes that happen to unmarshal: a
// balance and its history must be readable through the normal Repo
// methods after Restore, exactly as they were before Snapshot.
func TestSnapshotRestore_RoundTripsBalancesAndHistory(t *testing.T) {
	ctx := context.Background()
	original := ledger.NewMemLedger()
	acct := ledger.UserCoinsAccount("alice")
	seedCoins(t, original, acct, 500)
	_, _, err := original.PostTransaction(ctx, "gift", "spend-1", nil, []ledger.Entry{
		{AccountID: acct, Amount: -200, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: 200, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	data, err := original.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	restored := ledger.NewMemLedger()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	gotBalance, err := restored.Balance(ctx, acct, ledger.Coin)
	if err != nil {
		t.Fatalf("Balance after restore: %v", err)
	}
	wantBalance, _ := original.Balance(ctx, acct, ledger.Coin)
	if gotBalance != wantBalance {
		t.Fatalf("balance after restore = %d, want %d (matching pre-snapshot balance)", gotBalance, wantBalance)
	}

	gotHistory, _, err := restored.History(ctx, acct, "", 10)
	if err != nil {
		t.Fatalf("History after restore: %v", err)
	}
	if len(gotHistory) != 2 {
		t.Fatalf("expected 2 history entries after restore (seed + spend), got %d", len(gotHistory))
	}
}

// TestSnapshotRestore_IntegrityHoldsAfterRoundTrip proves a restored
// ledger still satisfies the whole-ledger zero-sum invariant — the
// balanceKey flattening/unflattening (structs aren't valid JSON map keys)
// is the one place a bug could silently corrupt account/currency pairing.
func TestSnapshotRestore_IntegrityHoldsAfterRoundTrip(t *testing.T) {
	original := ledger.NewMemLedger()
	seedCoins(t, original, ledger.UserCoinsAccount("alice"), 300)
	seedCoins(t, original, ledger.UserCoinsAccount("bob"), 700)

	data, err := original.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := ledger.NewMemLedger()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	sums := restored.IntegrityCheck()
	if sums[ledger.Coin] != 0 {
		t.Fatalf("restored ledger's coin sum = %d, want 0 (zero-sum invariant)", sums[ledger.Coin])
	}
	if got := sums[ledger.Coin]; got != 0 {
		t.Fatalf("unexpected nonzero sum after restore: %d", got)
	}
}
