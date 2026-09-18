package referralsvc_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lumena/referral"
	"github.com/lumena/referral/referralsvc"
)

func newService(t *testing.T, knownAccounts map[string]bool, creditCalls *int32) *referralsvc.Service {
	t.Helper()
	repo := referral.NewMemRepo()
	accountExists := func(_ context.Context, id string) (bool, error) {
		return knownAccounts[id], nil
	}
	creditCoins := func(_ context.Context, accountID string, amount int64, idempotencyKey string) (string, error) {
		if creditCalls != nil {
			atomic.AddInt32(creditCalls, 1)
		}
		return "tx-" + idempotencyKey, nil
	}
	return referralsvc.New(repo, accountExists, creditCoins)
}

func TestClaimCode_SelfReferralRejected(t *testing.T) {
	svc := newService(t, map[string]bool{"acc-1": true}, nil)
	_, err := svc.ClaimCode(context.Background(), "acc-1", "acc-1")
	if !errors.Is(err, referral.ErrSelfReferral) {
		t.Fatalf("expected ErrSelfReferral, got %v", err)
	}
}

func TestClaimCode_UnknownCodeRejected(t *testing.T) {
	svc := newService(t, map[string]bool{"acc-1": true}, nil)
	_, err := svc.ClaimCode(context.Background(), "acc-1", "does-not-exist")
	if !errors.Is(err, referral.ErrCodeNotFound) {
		t.Fatalf("expected ErrCodeNotFound, got %v", err)
	}
}

func TestClaimCode_CreditsReferrerAndRecordsHistory(t *testing.T) {
	var calls int32
	svc := newService(t, map[string]bool{"referrer": true, "newbie": true}, &calls)

	rec, err := svc.ClaimCode(context.Background(), "newbie", "referrer")
	if err != nil {
		t.Fatalf("ClaimCode: %v", err)
	}
	if rec.RewardCoins != referral.RewardCoins {
		t.Fatalf("reward = %d, want %d", rec.RewardCoins, referral.RewardCoins)
	}
	if calls != 1 {
		t.Fatalf("credit called %d times, want 1", calls)
	}

	history, err := svc.ListMyReferrals(context.Background(), "referrer")
	if err != nil {
		t.Fatalf("ListMyReferrals: %v", err)
	}
	if len(history) != 1 || history[0].ReferredID != "newbie" {
		t.Fatalf("unexpected history: %+v", history)
	}
}

func TestClaimCode_SecondClaimByOnceReferredAccountRejected(t *testing.T) {
	svc := newService(t, map[string]bool{"referrer-a": true, "referrer-b": true, "newbie": true}, nil)

	if _, err := svc.ClaimCode(context.Background(), "newbie", "referrer-a"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := svc.ClaimCode(context.Background(), "newbie", "referrer-b")
	if !errors.Is(err, referral.ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed on second claim, got %v", err)
	}
}

// TestClaimCode_ConcurrentDoubleClaimNeverDoubleCredits proves the
// TryClaim-before-credit ordering actually holds under real concurrency,
// not just in a single-threaded read of the code: many goroutines racing
// to claim on behalf of the SAME newly-referred account must result in
// exactly one successful claim and exactly one ledger credit call, never
// more — a double-credit here would be a real money bug.
func TestClaimCode_ConcurrentDoubleClaimNeverDoubleCredits(t *testing.T) {
	var calls int32
	svc := newService(t, map[string]bool{"referrer": true, "newbie": true}, &calls)

	const attempts = 50
	var wg sync.WaitGroup
	var successes int32
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.ClaimCode(context.Background(), "newbie", "referrer")
			if err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successful claims = %d, want exactly 1", successes)
	}
	if calls != 1 {
		t.Fatalf("ledger credited %d times, want exactly 1 (double-credit bug)", calls)
	}
}
